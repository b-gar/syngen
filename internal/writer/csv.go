package writer

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"time"
	"unsafe"

	"github.com/b-gar/syngen/internal/generator"
	"github.com/b-gar/syngen/internal/schema"
	"github.com/b-gar/syngen/internal/storage"
)

const (
	csvColOther uint8 = iota
	csvColDate
	csvColDatetime
)

func WriteCSV(filename string, tbl *schema.Table, targetRows int, gen *generator.Generator) (err error) {
	f, err := storage.CreateFile(filename)
	if err != nil {
		return err
	}
	defer closeOrAbortOutput(f, filename, &err)
	w := csv.NewWriter(f)
	defer func() {
		if err != nil {
			return
		}
		w.Flush()
		if flushErr := w.Error(); flushErr != nil {
			err = flushErr
		}
	}()
	if err := writeCSVHeaders(w, tbl.Columns); err != nil {
		return err
	}
	return writeCSVRows(w, tbl, targetRows, gen)
}

func writeCSVRows(w *csv.Writer, tbl *schema.Table, targetRows int, gen *generator.Generator) error {
	numCols := len(tbl.Columns)
	strRow := make([]string, numCols)
	colTags := make([]uint8, numCols)
	for i := range tbl.Columns {
		switch tbl.Columns[i].Type {
		case "date":
			colTags[i] = csvColDate
		case "datetime":
			colTags[i] = csvColDatetime
		default:
			colTags[i] = csvColOther
		}
	}
	scratch := make([]byte, 0, numCols*32)
	for r := range targetRows {
		row, err := gen.NextRow()
		if err != nil {
			return fmt.Errorf("error generating row %d: %w", r, err)
		}
		scratch = scratch[:0]
		for i, val := range row {
			strRow[i], scratch = appendCSVField(val, colTags[i], scratch)
		}
		if err := w.Write(strRow); err != nil {
			return fmt.Errorf("error writing csv row %d: %w", r, err)
		}
	}
	return nil
}

func writeCSVHeaders(w *csv.Writer, cols []schema.Column) error {
	headers := make([]string, len(cols))
	for i, col := range cols {
		headers[i] = col.Name
	}
	if err := w.Write(headers); err != nil {
		return fmt.Errorf("failed to write csv headers: %w", err)
	}
	return nil
}

func appendCSVField(val any, colTag uint8, scratch []byte) (string, []byte) {
	if val == nil {
		return "", scratch
	}
	start := len(scratch)
	switch v := val.(type) {
	case string:
		return v, scratch
	case int32:
		scratch = strconv.AppendInt(scratch, int64(v), 10)
	case int64:
		switch colTag {
		case csvColDate:
			scratch = time.UnixMilli(v).UTC().AppendFormat(scratch, time.DateOnly)
		case csvColDatetime:
			scratch = time.UnixMilli(v).UTC().AppendFormat(scratch, time.DateTime)
		default:
			scratch = strconv.AppendInt(scratch, v, 10)
		}
	case int:
		scratch = strconv.AppendInt(scratch, int64(v), 10)
	case float64:
		scratch = strconv.AppendFloat(scratch, v, 'f', -1, 64)
	case bool:
		if v {
			return "true", scratch
		}
		return "false", scratch
	default:
		return fmt.Sprintf("%v", v), scratch
	}
	b := scratch[start:]
	return unsafe.String(unsafe.SliceData(b), len(b)), scratch
}
