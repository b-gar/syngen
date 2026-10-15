package generator

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	data    []byte
	offsets []uint32
	nulls   []bool
}

type memoryParquetStreamer struct {
	mu           sync.Mutex
	r            storage.Reader
	size         int64
	requiredCols []string
	columns      map[string]*stringColumn
	totalRows    int64
	currentRow   int64
}

func NewMemoryParquetStreamer(r storage.Reader, size int64, requiredCols []string) (RowStreamer, error) {
	s := &memoryParquetStreamer{
		r:            r,
		size:         size,
		requiredCols: requiredCols,
		columns:      make(map[string]*stringColumn, len(requiredCols)),
	}
	for _, col := range requiredCols {
		s.columns[col] = &stringColumn{}
	}
	if err := s.Reset(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *memoryParquetStreamer) Reset() (err error) {
	s.totalRows = 0
	s.currentRow = 0
	pr, err := file.NewParquetReader(io.NewSectionReader(s.r, 0, s.size))
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
	pqColIndices, err := resolvePqColIndices(arrRdr, s.requiredCols)
	if err != nil {
		return err
	}
	rr, err := arrRdr.GetRecordReader(context.Background(), pqColIndices, nil)
	if err != nil {
		return err
	}
	defer rr.Release()
	s.initColumnBuffers(pr.NumRows())
	var colIndices map[string]int
	for rr.Next() {
		rec := rr.RecordBatch()
		numRows := int(rec.NumRows())
		s.totalRows += int64(numRows)
		if colIndices == nil {
			colIndices = mapSchemaFieldIndices(rec.Schema(), s.requiredCols)
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
	var pqColIndices []int
	for _, name := range requiredCols {
		indices := sc.FieldIndices(name)
		if len(indices) > 0 {
			pqColIndices = append(pqColIndices, indices[0])
		}
	}
	if pqColIndices == nil {
		pqColIndices = make([]int, 0)
	}
	return pqColIndices, nil
}

func (s *memoryParquetStreamer) initColumnBuffers(totalRows int64) {
	for _, col := range s.columns {
		col.data = make([]byte, 0, totalRows*10)
		col.offsets = make([]uint32, 1, totalRows+1)
		col.nulls = make([]bool, 0, totalRows)
	}
}

func mapSchemaFieldIndices(schema *arrow.Schema, requiredCols []string) map[string]int {
	colIndices := make(map[string]int, len(requiredCols))
	for _, colName := range requiredCols {
		for i := 0; i < schema.NumFields(); i++ {
			if schema.Field(i).Name == colName {
				colIndices[colName] = i
				break
			}
		}
	}
	return colIndices
}

func (s *memoryParquetStreamer) loadRecordBatch(rec arrow.RecordBatch, colIndices map[string]int, numRows int) error {
	for colName, colStruct := range s.columns {
		idx, ok := colIndices[colName]
		if !ok {
			for range numRows {
				colStruct.offsets = append(colStruct.offsets, colStruct.offsets[len(colStruct.offsets)-1])
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
				return fmt.Errorf("column '%s' byte data length %d overflows uint32 offset limit", colName, projectedLen)
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
	for _, colName := range s.requiredCols {
		col, ok := s.columns[colName]
		if !ok {
			dst[colName] = nil
			continue
		}
		if col.nulls[s.currentRow] {
			dst[colName] = nil
		} else {
			start := col.offsets[s.currentRow]
			end := col.offsets[s.currentRow+1]
			if start == end {
				dst[colName] = ""
			} else {
				dst[colName] = unsafe.String(unsafe.SliceData(col.data[start:end]), end-start)
			}
		}
	}
	s.currentRow++
	return nil
}

func (s *memoryParquetStreamer) Close() error {
	return s.r.Close()
}

func (s *memoryParquetStreamer) TotalRows() (int64, error) {
	return s.totalRows, nil
}
