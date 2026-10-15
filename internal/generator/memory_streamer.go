package generator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/b-gar/syngen/internal/storage"
)

type stringColumn struct {
	name    string
	data    []byte
	offsets []uint32
	nulls   []bool
}

type memoryParquetStreamer struct {
	mu         sync.Mutex
	columns    []stringColumn
	totalRows  int64
	currentRow int64
}

func NewMemoryParquetStreamer(r storage.Reader, size int64, requiredCols []string) (RowStreamer, error) {
	cols := make([]stringColumn, len(requiredCols))
	for i, name := range requiredCols {
		cols[i].name = name
	}
	s := &memoryParquetStreamer{columns: cols}
	if err := s.load(r, size, requiredCols); err != nil {
		return nil, err
	}
	// Release the underlying reader (and its 8MB cloud block cache) once columns are in memory.
	if err := r.Close(); err != nil {
		log.Printf("error closing reader after memory parquet load: %v", err)
	}
	return s, nil
}

func (s *memoryParquetStreamer) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentRow = 0
	return nil
}

func (s *memoryParquetStreamer) load(r storage.Reader, size int64, requiredCols []string) (err error) {
	pr, err := file.NewParquetReader(io.NewSectionReader(r, 0, size))
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, pr.Close())
	}()
	arrRdr, err := pqarrow.NewFileReader(pr, pqarrow.ArrowReadProperties{BatchSize: 10000}, memory.DefaultAllocator)
	if err != nil {
		return err
	}
	pqColIndices, err := resolvePqColIndices(arrRdr, requiredCols)
	if err != nil {
		return err
	}
	rr, err := arrRdr.GetRecordReader(context.Background(), pqColIndices, nil)
	if err != nil {
		return err
	}
	defer rr.Release()
	s.initColumnBuffers(pr.NumRows())
	var colIndices []int
	for rr.Next() {
		rec := rr.RecordBatch()
		numRows := int(rec.NumRows())
		s.totalRows += int64(numRows)
		if colIndices == nil {
			colIndices = mapSchemaFieldIndices(rec.Schema(), requiredCols)
		}
		if err := s.loadRecordBatch(rec, colIndices, numRows); err != nil {
			return err
		}
	}
	if err := rr.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func resolvePqColIndices(arrRdr *pqarrow.FileReader, requiredCols []string) ([]int, error) {
	if len(requiredCols) == 0 {
		return nil, nil
	}
	sc, err := arrRdr.Schema()
	if err != nil {
		return nil, err
	}
	pqColIndices := make([]int, 0, len(requiredCols))
	for _, name := range requiredCols {
		if indices := sc.FieldIndices(name); len(indices) > 0 {
			pqColIndices = append(pqColIndices, indices[0])
		}
	}
	return pqColIndices, nil
}

func (s *memoryParquetStreamer) initColumnBuffers(totalRows int64) {
	for i := range s.columns {
		col := &s.columns[i]
		col.data = make([]byte, 0, totalRows*10)
		col.offsets = make([]uint32, 1, totalRows+1)
		col.nulls = make([]bool, 0, totalRows)
	}
}

func mapSchemaFieldIndices(schema *arrow.Schema, requiredCols []string) []int {
	colIndices := make([]int, len(requiredCols))
	for i, colName := range requiredCols {
		colIndices[i] = -1
		if indices := schema.FieldIndices(colName); len(indices) > 0 {
			colIndices[i] = indices[0]
		}
	}
	return colIndices
}

func (s *memoryParquetStreamer) loadRecordBatch(rec arrow.RecordBatch, colIndices []int, numRows int) error {
	for colIdx := range s.columns {
		colStruct := &s.columns[colIdx]
		idx := colIndices[colIdx]
		if idx < 0 {
			lastOffset := colStruct.offsets[len(colStruct.offsets)-1]
			for range numRows {
				colStruct.offsets = append(colStruct.offsets, lastOffset)
				colStruct.nulls = append(colStruct.nulls, true)
			}
			continue
		}
		arr := rec.Column(idx)
		strArr, isStrArr := arr.(*array.String)
		for i := range numRows {
			var strVal string
			isNull := arr.IsNull(i)
			if !isNull {
				if isStrArr {
					strVal = strArr.Value(i)
				} else {
					strVal = arr.ValueStr(i)
				}
			}
			colStruct.nulls = append(colStruct.nulls, isNull)
			projectedLen := uint64(len(colStruct.data)) + uint64(len(strVal))
			if projectedLen > 0xFFFFFFFF {
				return fmt.Errorf("column '%s' byte data length %d overflows uint32 offset limit", colStruct.name, projectedLen)
			}
			colStruct.data = append(colStruct.data, strVal...)
			colStruct.offsets = append(colStruct.offsets, uint32(len(colStruct.data)))
		}
	}
	return nil
}

func (s *memoryParquetStreamer) SeekRow(offset int64, wrap bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if offset < 0 {
		return fmt.Errorf("invalid negative offset: %d", offset)
	}
	if offset >= s.totalRows {
		if wrap && s.totalRows > 0 {
			offset = offset % s.totalRows
		} else {
			return io.EOF
		}
	}
	s.currentRow = offset
	return nil
}

func (s *memoryParquetStreamer) NextRow(dst map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentRow >= s.totalRows {
		return io.EOF
	}
	row := s.currentRow
	for i := range s.columns {
		col := &s.columns[i]
		if col.nulls[row] {
			dst[col.name] = nil
			continue
		}
		start := col.offsets[row]
		end := col.offsets[row+1]
		if start == end {
			dst[col.name] = ""
		} else {
			dst[col.name] = unsafe.String(unsafe.SliceData(col.data[start:end]), end-start)
		}
	}
	s.currentRow = row + 1
	return nil
}

func (s *memoryParquetStreamer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.columns = nil
	return nil
}

func (s *memoryParquetStreamer) TotalRows() (int64, error) {
	return s.totalRows, nil
}
