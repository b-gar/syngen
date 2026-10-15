package writer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"github.com/b-gar/syngen/internal/generator"
	"github.com/b-gar/syngen/internal/schema"
)

func TestWriteParquetAndCSV_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	prec := 10
	scale := 2
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "id", Type: "bigint", Generator: schema.GeneratorConfig{Method: "row_number"}},
			{Name: "name", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name"}},
			{Name: "num", Type: "integer", Generator: schema.GeneratorConfig{Method: "range", Min: 1, Max: 100}},
			{Name: "amount", Type: "decimal", Precision: &prec, Scale: &scale, Generator: schema.GeneratorConfig{Method: "range", Min: 10.5, Max: 99.5}},
			{Name: "active", Type: "boolean"},
			{Name: "created_dt", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "-1y", Max: "0d"}},
			{Name: "epoch_prev_dt", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "1969-12-31", Max: "1969-12-31"}},
			{Name: "updated_ts", Type: "datetime", Generator: schema.GeneratorConfig{Method: "range", Min: "-1d", Max: "0d"}},
			{Name: "nullable_col", Type: "string", NullPercentage: 50},
		},
	}
	gen, err := generator.NewGenerator(tbl, 20, 0, 42, nil)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}
	defer func() {
		if err := gen.Close(); err != nil {
			t.Errorf("failed to close generator: %v", err)
		}
	}()
	pqPath := filepath.Join(tmpDir, "test.parquet")
	if err := WriteParquet(pqPath, tbl, 20, gen); err != nil {
		t.Fatalf("WriteParquet failed: %v", err)
	}
	verifyParquetOutput(t, pqPath)
	gen2, err := generator.NewGenerator(tbl, 20, 0, 42, nil)
	if err != nil {
		t.Fatalf("failed to create generator 2: %v", err)
	}
	defer func() {
		if err := gen2.Close(); err != nil {
			t.Errorf("failed to close generator 2: %v", err)
		}
	}()
	csvPath := filepath.Join(tmpDir, "test.csv")
	if err := WriteCSV(csvPath, tbl, 20, gen2); err != nil {
		t.Fatalf("WriteCSV failed: %v", err)
	}
	fi2, err := os.Stat(csvPath)
	if err != nil || fi2.Size() == 0 {
		t.Fatalf("CSV file was not written properly: %v", err)
	}
}

func verifyParquetOutput(t *testing.T, pqPath string) {
	t.Helper()
	f, err := os.Open(pqPath)
	if err != nil {
		t.Fatalf("failed to open written parquet file: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("failed to close file: %v", err)
		}
	}()
	pr, err := file.NewParquetReader(f)
	if err != nil {
		t.Fatalf("failed to create parquet reader: %v", err)
	}
	defer func() {
		if err := pr.Close(); err != nil {
			t.Errorf("failed to close parquet reader: %v", err)
		}
	}()
	arrRdr, err := pqarrow.NewFileReader(pr, pqarrow.ArrowReadProperties{BatchSize: 100}, memory.DefaultAllocator)
	if err != nil {
		t.Fatalf("failed to create arrow file reader: %v", err)
	}
	arrowSchema, err := arrRdr.Schema()
	if err != nil {
		t.Fatalf("failed to read arrow schema: %v", err)
	}
	expectedTypes := map[string]arrow.Type{
		"id":            arrow.INT64,
		"name":          arrow.STRING,
		"num":           arrow.INT32,
		"amount":        arrow.DECIMAL128,
		"active":        arrow.BOOL,
		"created_dt":    arrow.DATE32,
		"epoch_prev_dt": arrow.DATE32,
		"updated_ts":    arrow.TIMESTAMP,
		"nullable_col":  arrow.STRING,
	}
	for colName, expType := range expectedTypes {
		fields := arrowSchema.FieldIndices(colName)
		if len(fields) == 0 {
			t.Fatalf("missing column %s in parquet schema", colName)
		}
		actualType := arrowSchema.Field(fields[0]).Type.ID()
		if actualType != expType {
			t.Errorf("column %s has arrow type %s, want %s", colName, actualType, expType)
		}
	}
	rr, err := arrRdr.GetRecordReader(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("failed to get record reader: %v", err)
	}
	defer rr.Release()
	if !rr.Next() {
		t.Fatalf("expected at least one record batch from parquet, got none")
	}
	rec := rr.RecordBatch()
	if rec.NumRows() != 20 {
		t.Errorf("expected 20 rows, got %d", rec.NumRows())
	}
	idCol := rec.Column(0).(*array.Int64)
	if idCol.Value(0) != 1 {
		t.Errorf("expected id row 0 to be 1, got %d", idCol.Value(0))
	}
	epochDtIdx := arrowSchema.FieldIndices("epoch_prev_dt")[0]
	epochDtCol := rec.Column(epochDtIdx).(*array.Date32)
	if epochDtCol.Value(0) != arrow.Date32(-1) {
		t.Errorf("expected epoch_prev_dt (1969-12-31) to be Date32(-1), got %d", epochDtCol.Value(0))
	}
}

func TestDate32FromMillis(t *testing.T) {
	tests := []struct {
		name   string
		millis int64
		want   arrow.Date32
	}{
		{"epoch_start", 0, 0},
		{"one_day_after", 86400000, 1},
		{"partial_day_after", 86400000 + 1000, 1},
		{"one_day_before", -86400000, -1},
		{"partial_day_before_floor", -86400000 + 1000, -1},
		{"partial_day_before_midnight", -1, -1},
		{"two_days_before_partial", -86400000 - 1000, -2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := date32FromMillis(tt.millis)
			if got != tt.want {
				t.Errorf("date32FromMillis(%d) = %d, want %d", tt.millis, got, tt.want)
			}
		})
	}
}

func TestAppendDecimalField_PrecisionAndScale(t *testing.T) {
	pool := memory.NewGoAllocator()
	dt := &arrow.Decimal128Type{Precision: 5, Scale: 2}
	b := array.NewDecimal128Builder(pool, dt)
	defer b.Release()
	appendDecimalField(b, dt, int64(42))
	appendDecimalField(b, dt, int32(7))
	appendDecimalField(b, dt, float64(12.34))
	appendDecimalField(b, dt, int64(9999))
	arr := b.NewDecimal128Array()
	defer arr.Release()
	if arr.Value(0).LowBits() != 4200 {
		t.Errorf("expected int64(42) at scale 2 to have scaled value 4200, got %d", arr.Value(0).LowBits())
	}
	if arr.Value(1).LowBits() != 700 {
		t.Errorf("expected int32(7) at scale 2 to have scaled value 700, got %d", arr.Value(1).LowBits())
	}
	if arr.ValueStr(2) != "12.34" {
		t.Errorf("expected float64(12.34) to be 12.34, got %s", arr.ValueStr(2))
	}
	if !arr.IsNull(3) {
		t.Errorf("expected int64(9999) exceeding precision(5,2) to be null, got %s", arr.ValueStr(3))
	}
}

func TestParquetLookup_SequentialAndRandom_AllTypes(t *testing.T) {
	tmpDir := t.TempDir()
	prefix := tmpDir + "/"
	prec, scale := 10, 2
	parentTbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "id", Type: "bigint", Generator: schema.GeneratorConfig{Method: "row_number"}},
			{Name: "amount", Type: "decimal", Precision: &prec, Scale: &scale, Generator: schema.GeneratorConfig{Method: "constant", Value: 42.5}},
			{Name: "dt", Type: "date", Generator: schema.GeneratorConfig{Method: "constant", Value: "2024-06-15"}},
			{Name: "ts", Type: "datetime", Generator: schema.GeneratorConfig{Method: "constant", Value: "2024-06-15 12:30:45"}},
		},
	}
	parentGen, err := generator.NewGenerator(parentTbl, 5, 0, 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := parentGen.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if err := WriteParquet(prefix+"parent_part_0.parquet", parentTbl, 5, parentGen); err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{"next", "random"} {
		t.Run(sel, func(t *testing.T) {
			childTbl := &schema.Table{
				Sources: map[string]schema.SourceConfig{
					"p": {Table: "parent", Selection: sel},
				},
				Columns: []schema.Column{
					{Name: "id", Type: "bigint", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@p.id"}},
					{Name: "amount", Type: "decimal", Precision: &prec, Scale: &scale, Generator: schema.GeneratorConfig{Method: "lookup", Value: "@p.amount"}},
					{Name: "dt", Type: "date", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@p.dt"}},
					{Name: "ts", Type: "datetime", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@p.ts"}},
				},
			}
			if err := generator.ParseSchemaAST(childTbl); err != nil {
				t.Fatal(err)
			}
			sm, err := generator.NewSourceManager(childTbl, prefix, 0, "parquet", generator.FindRequiredColumns(childTbl))
			if err != nil {
				t.Fatal(err)
			}
			defer sm.Close()
			childGen, err := generator.NewGenerator(childTbl, 5, 0, 42, sm)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := childGen.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			}()
			for range 5 {
				if _, err := childGen.NextRow(); err != nil {
					t.Fatalf("failed to read %s lookup row: %v", sel, err)
				}
			}
		})
	}
}
