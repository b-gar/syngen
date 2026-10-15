package writer

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/b-gar/syngen/internal/generator"
	"github.com/b-gar/syngen/internal/schema"
	"github.com/b-gar/syngen/internal/storage"
)

var pow10Table = [19]int64{
	1, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000, 1000000000,
	10000000000, 100000000000, 1000000000000, 10000000000000, 100000000000000,
	1000000000000000, 10000000000000000, 100000000000000000, 1000000000000000000,
}

func date32FromMillis(millis int64) arrow.Date32 {
	days := millis / 86400000
	if millis < 0 && millis%86400000 != 0 {
		days--
	}
	return arrow.Date32(days)
}

func parseInt64Fast(s string) (int64, bool) {
	if len(s) == 0 || len(s) > 18 {
		i, err := strconv.ParseInt(s, 10, 64)
		return i, err == nil
	}
	neg := false
	idx := 0
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		idx = 1
		if len(s) == 1 {
			return 0, false
		}
	}
	var n int64
	for ; idx < len(s); idx++ {
		c := s[idx] - '0'
		if c > 9 {
			return 0, false
		}
		n = n*10 + int64(c)
	}
	if neg {
		return -n, true
	}
	return n, true
}

func appendStringField(f *array.StringBuilder, val any) {
	switch v := val.(type) {
	case string:
		f.Append(v)
	case int64:
		f.Append(strconv.FormatInt(v, 10))
	case int32:
		f.Append(strconv.FormatInt(int64(v), 10))
	case int:
		f.Append(strconv.Itoa(v))
	case float64:
		f.Append(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		if v {
			f.Append("true")
		} else {
			f.Append("false")
		}
	default:
		f.Append(fmt.Sprintf("%v", val))
	}
}

func appendInt64Field(f *array.Int64Builder, val any) {
	switch v := val.(type) {
	case int64:
		f.Append(v)
	case int32:
		f.Append(int64(v))
	case int:
		f.Append(int64(v))
	case string:
		if ms, ok := generator.ParseDateStringToMillis(v); ok {
			f.Append(ms)
		} else {
			f.AppendNull()
		}
	default:
		f.AppendNull()
	}
}

func appendInt32Field(f *array.Int32Builder, val any) {
	switch v := val.(type) {
	case int64:
		f.Append(int32(v))
	case int32:
		f.Append(v)
	case int:
		f.Append(int32(v))
	case float64:
		f.Append(int32(v))
	case string:
		if i, err := strconv.ParseInt(v, 10, 32); err == nil {
			f.Append(int32(i))
		} else {
			f.AppendNull()
		}
	default:
		f.AppendNull()
	}
}

func appendDateField(f *array.Date32Builder, val any) {
	switch v := val.(type) {
	case int64:
		f.Append(date32FromMillis(v))
	case int32:
		f.Append(date32FromMillis(int64(v)))
	case int:
		f.Append(date32FromMillis(int64(v)))
	case string:
		if ms, ok := generator.ParseDateStringToMillis(v); ok {
			f.Append(date32FromMillis(ms))
		} else {
			f.AppendNull()
		}
	default:
		f.AppendNull()
	}
}

func appendTimestampField(f *array.TimestampBuilder, val any) {
	switch v := val.(type) {
	case int64:
		f.Append(arrow.Timestamp(v))
	case int32:
		f.Append(arrow.Timestamp(int64(v)))
	case int:
		f.Append(arrow.Timestamp(int64(v)))
	case string:
		if ms, ok := generator.ParseDateStringToMillis(v); ok {
			f.Append(arrow.Timestamp(ms))
		} else {
			f.AppendNull()
		}
	default:
		f.AppendNull()
	}
}

func appendDecimalInt64(f *array.Decimal128Builder, dt *arrow.Decimal128Type, v int64) {
	if dt.Scale == 0 {
		if dt.Precision >= 19 {
			f.Append(decimal128.FromI64(v))
			return
		}
		maxAbs := pow10Table[dt.Precision] - 1
		if v >= -maxAbs && v <= maxAbs {
			f.Append(decimal128.FromI64(v))
		} else {
			f.AppendNull()
		}
		return
	}
	if dt.Precision <= 18 && dt.Scale >= 0 && int(dt.Scale) <= 18 {
		scaleMult := pow10Table[dt.Scale]
		maxUnscaled := (pow10Table[dt.Precision] - 1) / scaleMult
		if v >= -maxUnscaled && v <= maxUnscaled {
			f.Append(decimal128.FromI64(v * scaleMult))
		} else {
			f.AppendNull()
		}
		return
	}
	if dec, err := decimal128.FromString(strconv.FormatInt(v, 10), dt.Precision, dt.Scale); err == nil {
		f.Append(dec)
	} else {
		f.AppendNull()
	}
}

func appendDecimalFloat64(f *array.Decimal128Builder, dt *arrow.Decimal128Type, v float64) {
	if dt.Precision <= 15 && dt.Scale >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
		scaleMult := float64(pow10Table[dt.Scale])
		maxAbs := float64(pow10Table[dt.Precision] - 1)
		scaled := math.Round(v * scaleMult)
		if scaled >= -maxAbs && scaled <= maxAbs {
			f.Append(decimal128.FromI64(int64(scaled)))
		} else {
			f.AppendNull()
		}
		return
	}
	if dec, err := decimal128.FromFloat64(v, dt.Precision, dt.Scale); err == nil {
		f.Append(dec)
	} else {
		f.AppendNull()
	}
}

func appendDecimalField(f *array.Decimal128Builder, dt *arrow.Decimal128Type, val any) {
	switch v := val.(type) {
	case int64:
		appendDecimalInt64(f, dt, v)
	case int32:
		appendDecimalInt64(f, dt, int64(v))
	case int:
		appendDecimalInt64(f, dt, int64(v))
	case float64:
		appendDecimalFloat64(f, dt, v)
	case string:
		if dt.Scale == 0 {
			if i, ok := parseInt64Fast(v); ok {
				appendDecimalInt64(f, dt, i)
				return
			}
		}
		if dec, err := decimal128.FromString(v, dt.Precision, dt.Scale); err == nil {
			f.Append(dec)
		} else {
			f.AppendNull()
		}
	default:
		f.AppendNull()
	}
}

type fieldAppender func(val any)

func wrapNullGuard[B array.Builder](b B, fn func(B, any)) fieldAppender {
	return func(val any) {
		if val == nil {
			b.AppendNull()
			return
		}
		fn(b, val)
	}
}

func buildFieldAppender(field array.Builder) fieldAppender {
	switch f := field.(type) {
	case *array.StringBuilder:
		return wrapNullGuard(f, appendStringField)
	case *array.Int64Builder:
		return wrapNullGuard(f, appendInt64Field)
	case *array.Int32Builder:
		return wrapNullGuard(f, appendInt32Field)
	case *array.Float64Builder:
		return wrapNullGuard(f, func(b *array.Float64Builder, v any) {
			if fl, ok := v.(float64); ok {
				b.Append(fl)
			} else {
				b.AppendNull()
			}
		})
	case *array.Decimal128Builder:
		dt := f.Type().(*arrow.Decimal128Type)
		return wrapNullGuard(f, func(b *array.Decimal128Builder, v any) {
			appendDecimalField(b, dt, v)
		})
	case *array.BooleanBuilder:
		return wrapNullGuard(f, func(b *array.BooleanBuilder, v any) {
			if bl, ok := v.(bool); ok {
				b.Append(bl)
			} else {
				b.AppendNull()
			}
		})
	case *array.Date32Builder:
		return wrapNullGuard(f, appendDateField)
	case *array.TimestampBuilder:
		return wrapNullGuard(f, appendTimestampField)
	default:
		return func(val any) {
			f.AppendNull()
		}
	}
}

func closeOrAbortOutput(f io.WriteCloser, filename string, err *error) {
	if *err != nil {
		if aborter, ok := f.(interface{ Abort(error) }); ok {
			aborter.Abort(*err)
			return
		}
	}
	if closeErr := f.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		if *err == nil {
			*err = closeErr
		} else {
			log.Printf("error closing file %s: %v", filename, closeErr)
		}
	}
}

func WriteParquet(filename string, tbl *schema.Table, targetRows int, gen *generator.Generator) (err error) {
	f, err := storage.CreateFile(filename)
	if err != nil {
		return err
	}
	defer closeOrAbortOutput(f, filename, &err)
	sc := buildArrowSchema(tbl.Columns)
	writerProps := parquet.NewWriterProperties(
		parquet.WithCompression(compress.Codecs.Snappy),
		parquet.WithMaxRowGroupLength(10000), // Flush row groups aggressively to keep memory under 50MiB
	)
	arrowProps := pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema())
	pw, err := pqarrow.NewFileWriter(sc, f, writerProps, arrowProps)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			return
		}
		if closeErr := pw.Close(); closeErr != nil {
			err = closeErr
		}
	}()
	pool := memory.NewGoAllocator()
	b := array.NewRecordBuilder(pool, sc)
	defer b.Release()
	return writeParquetRows(pw, b, targetRows, gen)
}

func writeParquetRows(pw *pqarrow.FileWriter, b *array.RecordBuilder, targetRows int, gen *generator.Generator) error {
	const chunkSize = 10000
	rowCount := 0
	fields := b.Fields()
	appenders := make([]fieldAppender, len(fields))
	for i, f := range fields {
		appenders[i] = buildFieldAppender(f)
	}
	b.Reserve(min(targetRows, chunkSize))
	for r := range targetRows {
		row, err := gen.NextRow()
		if err != nil {
			return fmt.Errorf("error generating row %d: %w", r, err)
		}
		for i, val := range row {
			appenders[i](val)
		}
		rowCount++
		if rowCount >= chunkSize {
			if err := flushRecordBatch(pw, b); err != nil {
				return err
			}
			rowCount = 0
			if rem := targetRows - r - 1; rem > 0 {
				b.Reserve(min(rem, chunkSize))
			}
		}
	}
	if rowCount > 0 {
		return flushRecordBatch(pw, b)
	}
	return nil
}

func buildArrowSchema(cols []schema.Column) *arrow.Schema {
	fields := make([]arrow.Field, len(cols))
	for i, col := range cols {
		fields[i] = arrow.Field{Name: col.Name, Type: arrowTypeForColumn(col), Nullable: true}
	}
	return arrow.NewSchema(fields, nil)
}

func arrowTypeForColumn(col schema.Column) arrow.DataType {
	switch col.Type {
	case "string":
		return arrow.BinaryTypes.String
	case "integer":
		return arrow.PrimitiveTypes.Int32
	case "bigint":
		return arrow.PrimitiveTypes.Int64
	case "decimal":
		if col.Precision != nil && col.Scale != nil {
			return &arrow.Decimal128Type{Precision: int32(*col.Precision), Scale: int32(*col.Scale)}
		}
		return arrow.PrimitiveTypes.Float64
	case "boolean":
		return arrow.FixedWidthTypes.Boolean
	case "date":
		return arrow.FixedWidthTypes.Date32
	case "datetime":
		return arrow.FixedWidthTypes.Timestamp_ms
	default:
		return arrow.BinaryTypes.String
	}
}

func flushRecordBatch(pw *pqarrow.FileWriter, b *array.RecordBuilder) error {
	rec := b.NewRecordBatch()
	defer rec.Release()
	return pw.Write(rec)
}
