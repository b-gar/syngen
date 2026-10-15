package generator

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/b-gar/syngen/internal/storage"
)

type RowStreamer interface {
	NextRow(dst map[string]any) error
	Reset() error
	Close() error
	SeekRow(offset int64, wrap bool) error
	TotalRows() (int64, error)
}

func buildRequiredColsMap(cols []string) map[string]bool {
	if len(cols) == 0 {
		return nil
	}
	m := make(map[string]bool, len(cols))
	for _, c := range cols {
		m[c] = true
	}
	return m
}

type csvStreamer struct {
	mu           sync.Mutex
	r            storage.Reader
	size         int64
	csvRdr       *csv.Reader
	headers      []string
	offsets      []int64
	totalRows    int64
	indexBuilt   bool
	requiredCols map[string]bool
}

func NewCSVStreamer(r storage.Reader, size int64, requiredCols []string) (RowStreamer, error) {
	s := &csvStreamer{r: r, size: size, requiredCols: buildRequiredColsMap(requiredCols)}
	if err := s.Reset(); err != nil {
		return nil, err
	}
	return s, nil
}

func newSectionCSVReader(r io.ReaderAt, off, n int64) *csv.Reader {
	rdr := csv.NewReader(io.NewSectionReader(r, off, n))
	rdr.LazyQuotes = true
	rdr.FieldsPerRecord = -1
	rdr.ReuseRecord = true
	return rdr
}

func (s *csvStreamer) Reset() error {
	rdr := newSectionCSVReader(s.r, 0, s.size)
	headers, err := rdr.Read()
	if err != nil {
		return fmt.Errorf("failed to read csv headers: %w", err)
	}
	s.headers = append([]string(nil), headers...)
	s.csvRdr = rdr
	return nil
}

func (s *csvStreamer) NextRow(dst map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.csvRdr.Read()
	if err == io.EOF {
		return io.EOF
	}
	if err != nil {
		return err
	}
	s.populateDst(record, dst)
	return nil
}

func (s *csvStreamer) populateDst(record []string, dst map[string]any) {
	for i, header := range s.headers {
		if s.requiredCols != nil && !s.requiredCols[header] {
			continue
		}
		if i < len(record) {
			dst[header] = record[i]
		}
	}
}

func (s *csvStreamer) Close() error {
	return s.r.Close()
}

func (s *csvStreamer) buildIndex() error {
	if s.indexBuilt {
		return nil
	}
	rdr := newSectionCSVReader(s.r, 0, s.size)
	if _, err := rdr.Read(); err != nil {
		return err
	}
	s.offsets = make([]int64, 0)
	for {
		offset := rdr.InputOffset()
		_, err := rdr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		s.offsets = append(s.offsets, offset)
	}
	s.totalRows = int64(len(s.offsets))
	s.indexBuilt = true
	return nil
}

func (s *csvStreamer) TotalRows() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.buildIndex(); err != nil {
		return 0, err
	}
	return s.totalRows, nil
}

func (s *csvStreamer) SeekRow(offset int64, wrap bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if offset < 0 {
		return fmt.Errorf("invalid negative offset: %d", offset)
	}
	if err := s.buildIndex(); err != nil {
		return err
	}
	if s.totalRows == 0 {
		return nil
	}
	if wrap {
		offset = offset % s.totalRows
	}
	if offset >= s.totalRows {
		return fmt.Errorf("csv seek offset %d exceeds total rows %d", offset, s.totalRows)
	}
	byteOffset := s.offsets[offset]
	s.csvRdr = newSectionCSVReader(s.r, byteOffset, s.size-byteOffset)
	return nil
}

type parquetStreamer struct {
	mu           sync.Mutex
	r            storage.Reader
	size         int64
	pr           *file.Reader
	rr           pqarrow.RecordReader
	currentRec   arrow.RecordBatch
	batchIdx     int
	batchRows    int
	requiredCols []string
}

func NewParquetStreamer(r storage.Reader, size int64, requiredCols []string) (RowStreamer, error) {
	s := &parquetStreamer{r: r, size: size, requiredCols: requiredCols}
	if err := s.Reset(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *parquetStreamer) closeActiveReaders() error {
	if s.currentRec != nil {
		s.currentRec.Release()
		s.currentRec = nil
	}
	if s.rr != nil {
		s.rr.Release()
		s.rr = nil
	}
	if s.pr != nil {
		err := s.pr.Close()
		s.pr = nil
		return err
	}
	return nil
}

func (s *parquetStreamer) Reset() error {
	closeErr := s.closeActiveReaders()
	pr, err := file.NewParquetReader(io.NewSectionReader(s.r, 0, s.size))
	if err != nil {
		return errors.Join(closeErr, err)
	}
	arrRdr, err := pqarrow.NewFileReader(pr, pqarrow.ArrowReadProperties{BatchSize: 10000}, memory.DefaultAllocator)
	if err != nil {
		return errors.Join(closeErr, err, pr.Close())
	}
	colIndices, err := resolvePqColIndices(arrRdr, s.requiredCols)
	if err != nil {
		return errors.Join(closeErr, err, pr.Close())
	}
	rr, err := arrRdr.GetRecordReader(context.Background(), colIndices, nil)
	if err != nil {
		return errors.Join(closeErr, err, pr.Close())
	}
	s.pr = pr
	s.rr = rr
	s.batchIdx = 0
	s.batchRows = 0
	return closeErr
}

func extractArrowValue(col arrow.Array, rowIdx int) any {
	switch c := col.(type) {
	case *array.String:
		return c.Value(rowIdx)
	case *array.Int64:
		return c.Value(rowIdx)
	case *array.Int32:
		return int64(c.Value(rowIdx))
	case *array.Float64:
		return c.Value(rowIdx)
	case *array.Boolean:
		return c.Value(rowIdx)
	case *array.Date32:
		return int64(c.Value(rowIdx)) * 86400000
	case *array.Timestamp:
		return int64(c.Value(rowIdx))
	default:
		return col.ValueStr(rowIdx)
	}
}

func (s *parquetStreamer) NextRow(dst map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentRec == nil || s.batchIdx >= s.batchRows {
		if !s.rr.Next() {
			if err := s.rr.Err(); err != nil {
				return err
			}
			return io.EOF
		}
		if s.currentRec != nil {
			s.currentRec.Release()
		}
		s.currentRec = s.rr.RecordBatch()
		s.currentRec.Retain()
		s.batchRows = int(s.currentRec.NumRows())
		s.batchIdx = 0
	}
	sc := s.currentRec.Schema()
	for colIdx := 0; colIdx < int(s.currentRec.NumCols()); colIdx++ {
		name := sc.Field(colIdx).Name
		col := s.currentRec.Column(colIdx)
		if col.IsNull(s.batchIdx) {
			dst[name] = nil
		} else {
			dst[name] = extractArrowValue(col, s.batchIdx)
		}
	}
	s.batchIdx++
	return nil
}

func (s *parquetStreamer) Close() error {
	return errors.Join(s.closeActiveReaders(), s.r.Close())
}

func (s *parquetStreamer) SeekRow(offset int64, wrap bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if offset < 0 {
		return fmt.Errorf("invalid negative offset: %d", offset)
	}
	total := s.pr.NumRows()
	if total == 0 {
		return nil
	}
	if wrap {
		offset = offset % total
	} else if offset >= total {
		return io.EOF
	}
	// Reset batch buffer since we are seeking
	if s.currentRec != nil {
		s.currentRec.Release()
		s.currentRec = nil
	}
	s.batchRows = 0
	s.batchIdx = 0
	return s.rr.SeekToRow(offset)
}

func (s *parquetStreamer) TotalRows() (int64, error) {
	return s.pr.NumRows(), nil
}
