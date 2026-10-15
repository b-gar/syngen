package generator

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/b-gar/syngen/internal/schema"
	"github.com/brianvoe/gofakeit/v7"
)

const (
	errParseAST   = "failed to parse schema AST: %v"
	errCloseGen   = "failed to close generator: %v"
	errEnsureType = "ensureType error: %v"
)

func setupGenerator(t *testing.T, tbl *schema.Table, numRows int) *Generator {
	t.Helper()
	if err := ParseSchemaAST(tbl); err != nil {
		t.Fatalf(errParseAST, err)
	}
	gen, err := NewGenerator(tbl, numRows, 0, 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gen.Close(); err != nil {
			t.Errorf(errCloseGen, err)
		}
	})
	return gen
}

func parsedSingleColTable(t *testing.T, col schema.Column) *schema.Table {
	t.Helper()
	tbl := &schema.Table{Columns: []schema.Column{col}}
	if err := ParseSchemaAST(tbl); err != nil {
		t.Fatalf(errParseAST, err)
	}
	return tbl
}

func setupSingleCol(t *testing.T, col schema.Column, numRows int) *Generator {
	t.Helper()
	return setupGenerator(t, parsedSingleColTable(t, col), numRows)
}

func assertAllRowsEqual(t *testing.T, gen *Generator, count int, expected any) {
	t.Helper()
	for range count {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		if row[0] != expected {
			t.Errorf("expected %v, got %v", expected, row[0])
		}
	}
}

func TestEnsureType(t *testing.T) {
	tests := []struct {
		name     string
		dataType string
		val      any
		want     any
		isDate   bool
	}{
		// Integer
		{"int_float64", "integer", float64(42), int32(42), false},
		{"int_int", "integer", 42, int32(42), false},
		{"int_string", "integer", "42", int32(42), false},
		{"int_negative", "integer", float64(-10), int32(-10), false},
		// Bigint
		{"bigint_float64", "bigint", float64(42), int64(42), false},
		{"bigint_int", "bigint", 42, int64(42), false},
		{"bigint_string", "bigint", "42", int64(42), false},
		{"bigint_negative", "bigint", float64(-10), int64(-10), false},
		// Decimal
		{"dec_float64", "decimal", float64(3.14), 3.14, false},
		{"dec_int", "decimal", 42, 42.0, false},
		{"dec_string", "decimal", "3.14", float64(3.14), false},
		// Date
		{"date_string", "date", "2024-01-15", nil, true},
		// Passthrough
		{"string_passthrough", "string", "hello", "hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ensureType(tt.val, tt.dataType, false)
			if err != nil {
				t.Fatalf(errEnsureType, err)
			}
			if tt.isDate {
				ms, ok := got.(int64)
				if !ok {
					t.Fatalf("ensureType(date) returned %T, want int64", got)
				}
				parsed := time.UnixMilli(ms).UTC()
				if parsed.Year() != 2024 || parsed.Month() != 1 || parsed.Day() != 15 {
					t.Errorf("ensureType(2024-01-15, date) = %v, want 2024-01-15", parsed)
				}
			} else if got != tt.want {
				t.Errorf("ensureType(%v, %s) = %v, want %v", tt.val, tt.dataType, got, tt.want)
			}
		})
	}
}

func TestEnsureType_ValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		dataType string
		val      any
	}{
		{"int_invalid_string", "integer", "abc"},
		{"int_overflow_int64", "integer", "9999999999999999999999"},
		{"int_overflow_int32", "integer", int64(math.MaxInt32 + 1)},
		{"int_underflow_int32", "integer", int64(math.MinInt32 - 1)},
		{"int_nan", "integer", math.NaN()},
		{"int_unsupported_type", "integer", []int{1, 2}},
		{"bigint_invalid_string", "bigint", "abc"},
		{"bigint_overflow_int64", "bigint", "9999999999999999999999"},
		{"bigint_nan", "bigint", math.NaN()},
		{"bigint_unsupported_type", "bigint", []int{1, 2}},
		{"decimal_invalid_string", "decimal", "not_a_float"},
		{"decimal_nan_string", "decimal", "NaN"},
		{"decimal_inf_string", "decimal", "Inf"},
		{"decimal_nan", "decimal", math.NaN()},
		{"decimal_unsupported_type", "decimal", []int{1, 2}},
		{"bool_invalid_string", "boolean", "not_a_bool"},
		{"bool_unsupported_type", "boolean", []int{1, 2}},
		{"date_invalid_string", "date", "not_a_date"},
		{"date_unsupported_type", "date", []int{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ensureType(tt.val, tt.dataType, false)
			if err == nil {
				t.Errorf("ensureType(%v, %s) expected error, got %v", tt.val, tt.dataType, got)
			}
		})
	}
}

func testDateBounds(t *testing.T, tests []struct {
	name string
	val  string
	want time.Time
},
) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDateBound(tt.val)
			if err != nil {
				t.Fatalf("parseDateBound(%s) returned unexpected error: %v", tt.val, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseDateBound(%s) = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}

func TestParseDateBound_Fixed(t *testing.T) {
	testDateBounds(t, []struct {
		name string
		val  string
		want time.Time
	}{
		{"date", "2024-06-15", time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"datetime", "2024-06-15 10:30:00", time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)},
	})
}

func TestParseDateBound_Offsets(t *testing.T) {
	now := time.Now().Truncate(24 * time.Hour)
	testDateBounds(t, []struct {
		name string
		val  string
		want time.Time
	}{
		{"today", "0d", now},
		{"yesterday", "-1d", now.AddDate(0, 0, -1)},
		{"next_month", "+1m", now.AddDate(0, 1, 0)},
		{"last_year", "-1y", now.AddDate(-1, 0, 0)},
	})
}

func TestParseDateBound_Invalid(t *testing.T) {
	if _, err := parseDateBound(""); err == nil {
		t.Error("expected error for empty date bound")
	}
	if _, err := parseDateBound("invalid-date"); err == nil {
		t.Error("expected error for invalid-date bound")
	}
}

func testDynamicDateRefTable(t *testing.T, colType, minStart, maxStart, minEnd, maxEnd string) (any, any) {
	t.Helper()
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "start", Type: colType, Generator: schema.GeneratorConfig{Method: "range", Min: minStart, Max: maxStart}},
			{Name: "end", Type: colType, Generator: schema.GeneratorConfig{Method: "range", Min: minEnd, Max: maxEnd}},
		},
	}
	gen := setupGenerator(t, tbl, 5)
	row, err := gen.NextRow()
	if err != nil {
		t.Fatalf("failed to generate row: %v", err)
	}
	return row[0], row[1]
}

func TestGenerateValue_DynamicDateRef(t *testing.T) {
	r0, r1 := testDynamicDateRefTable(t, "date", "2020-01-01", "2020-01-01", "@start +1y", "@start +2y")
	bgnMs, ok1 := r0.(int64)
	endMs, ok2 := r1.(int64)
	if !ok1 || !ok2 {
		t.Fatalf("expected int64 dates, got %T and %T", r0, r1)
	}
	bgn := time.UnixMilli(bgnMs).UTC()
	end := time.UnixMilli(endMs).UTC()
	wantBgn := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if !bgn.Equal(wantBgn) {
		t.Errorf("start = %v, want %v", bgn, wantBgn)
	}
	minExpected := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	maxExpected := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	if end.Before(minExpected) || end.After(maxExpected) {
		t.Errorf("end = %v, want between %v and %v", end, minExpected, maxExpected)
	}
}

func TestGenerateValue_DynamicDateRef_StringRange(t *testing.T) {
	r0, r1 := testDynamicDateRefTable(t, "string", "2024-01-01", "2024-01-01", "@start +1w", "@start +2w")
	startStr, ok1 := r0.(string)
	endStr, ok2 := r1.(string)
	if !ok1 || !ok2 {
		t.Fatalf("expected string dates, got %T and %T", r0, r1)
	}
	if startStr != "2024-01-01" {
		t.Errorf("start = %s, want 2024-01-01", startStr)
	}
	if len(endStr) != 10 {
		t.Errorf("end format invalid: %s", endStr)
	}
}

func TestParseDateOrDynamicBound_Overflow(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "start", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "2020-01-01", Max: "2020-01-01"}},
			{Name: "end", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "@start +999999999999999999999y", Max: "+1y"}},
		},
	}
	if err := ParseSchemaAST(tbl); err == nil {
		t.Error("expected error for overflowing dynamic offset in ParseSchemaAST")
	}
}

func TestGenerateValue_NullPercentage(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "test", Type: "string", NullPercentage: 100}, 10)
	row, err := gen.NextRow()
	if err != nil {
		t.Fatal(err)
	}
	if row[0] != nil {
		t.Errorf("100%% null_percentage should always return nil, got %v", row[0])
	}
}

func TestGenerateValue_NullValue(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "test", Type: "string", NullPercentage: 100, NullValue: "~"}, 10)
	row, err := gen.NextRow()
	if err != nil {
		t.Fatal(err)
	}
	if row[0] != "~" {
		t.Errorf("null_value=~ should return ~, got %v", row[0])
	}
}

func TestGenerateValue_Constant(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "status", Type: "string", Generator: schema.GeneratorConfig{Method: "constant", Value: "Active"}}, 10)
	assertAllRowsEqual(t, gen, 5, "Active")
}

func TestGenerateValue_RowNumber(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "id", Type: "integer", Generator: schema.GeneratorConfig{Method: "row_number"}}, 5)
	for i := range 5 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		if row[0] != int64(i+1) {
			t.Errorf("row_number row %d = %v, want %d", i, row[0], i+1)
		}
	}
}

func TestGenerateValue_RowNumber_ExceedsInt32(t *testing.T) {
	tbl := parsedSingleColTable(t, schema.Column{Name: "id", Type: "integer", Generator: schema.GeneratorConfig{Method: "row_number"}})
	if _, err := NewGenerator(tbl, 1, math.MaxInt32+1, 42, nil); err == nil {
		t.Fatal("expected error when partition row index exceeds max int32")
	}
}

func TestNewGenerator_BoundsOverflow(t *testing.T) {
	tbl := parsedSingleColTable(t, schema.Column{Name: "id", Type: "bigint", Generator: schema.GeneratorConfig{Method: "row_number"}})
	if _, err := NewGenerator(tbl, 2, math.MaxInt64/2+1, 42, nil); err == nil {
		t.Fatal("expected error when partition * rowCount overflows int64")
	}
	if _, err := NewGenerator(tbl, 2, math.MaxInt64-1, 42, nil); err == nil {
		t.Fatal("expected error when start + rowCount overflows int64")
	}
}

type valMockStreamer struct {
	val any
}

func (m *valMockStreamer) NextRow(dst map[string]any) error      { dst["num"] = m.val; return nil }
func (m *valMockStreamer) Reset() error                          { return nil }
func (m *valMockStreamer) Close() error                          { return nil }
func (m *valMockStreamer) SeekRow(offset int64, wrap bool) error { return nil }
func (m *valMockStreamer) TotalRows() (int64, error)             { return 10, nil }

func testLookupError(t *testing.T, colName, colType string, mockVal any) error {
	t.Helper()
	tbl := &schema.Table{
		Sources: map[string]schema.SourceConfig{
			"src": {Selection: "random"},
		},
		Columns: []schema.Column{
			{Name: colName, Type: colType, Generator: schema.GeneratorConfig{Method: "lookup", Value: "@src.num"}},
		},
	}
	if err := ParseSchemaAST(tbl); err != nil {
		t.Fatalf(errParseAST, err)
	}
	sm := &SourceManager{
		Configs: tbl.Sources,
		RandomStreamers: map[string]RowStreamer{
			"src": &valMockStreamer{val: mockVal},
		},
		RandomTotals: map[string]int64{
			"src": 10,
		},
	}
	gen, err := NewGenerator(tbl, 1, 0, 42, sm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gen.Close(); err != nil {
			t.Errorf(errCloseGen, err)
		}
	})
	_, err = gen.NextRow()
	return err
}

func TestGenerateValue_Lookup_TypeMismatchPropagation(t *testing.T) {
	err := testLookupError(t, "int_col", "integer", int64(math.MaxInt32+100))
	if err == nil {
		t.Fatal("expected NextRow error on int32 overflow in lookup")
	}
	if !strings.Contains(err.Error(), "src") || !strings.Contains(err.Error(), "int_col") {
		t.Errorf("expected error to mention source and column, got: %v", err)
	}
}

func TestGenerateValue_Lookup_InvalidStringPropagation(t *testing.T) {
	err := testLookupError(t, "bigint_col", "bigint", "not-a-number")
	if err == nil {
		t.Fatal("expected NextRow error on invalid bigint string in lookup")
	}
}

func TestGenerateValue_Choices(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "flag", Type: "string", Generator: schema.GeneratorConfig{
		Method:  "choices",
		Choices: []any{"Y", "N"},
	}}, 100)
	for range 100 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		if row[0] != "Y" && row[0] != "N" {
			t.Errorf("choices should return Y or N, got %v", row[0])
		}
	}
}

func TestGenerateValue_WeightedChoices(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "flag", Type: "string", Generator: schema.GeneratorConfig{
		Method:  "choices",
		Choices: []any{"A", "B"},
		Weights: []int{100, 0},
	}}, 100)
	assertAllRowsEqual(t, gen, 100, "A")
}

func TestGenerateValue_Range_Numeric(t *testing.T) {
	tests := []struct {
		name    string
		colType string
		min     any
		max     any
		minVal  int64
		maxVal  int64
	}{
		{"integer", "integer", float64(18), float64(65), 18, 65},
		{"bigint", "bigint", float64(10000000000), float64(99999999999), 10000000000, 99999999999},
		{"integer_bounds", "integer", int64(math.MinInt32), int64(math.MaxInt32), math.MinInt32, math.MaxInt32},
		{"bigint_bounds", "bigint", int64(math.MinInt64), int64(math.MaxInt64), math.MinInt64, math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := setupSingleCol(t, schema.Column{
				Name: "c1", Type: tt.colType, Generator: schema.GeneratorConfig{Method: "range", Min: tt.min, Max: tt.max},
			}, 100)
			for range 100 {
				row, err := gen.NextRow()
				if err != nil {
					t.Fatal(err)
				}
				var v int64
				switch val := row[0].(type) {
				case int32:
					v = int64(val)
				case int64:
					v = val
				default:
					t.Fatalf("unexpected type %T", row[0])
				}
				if v < tt.minVal || v > tt.maxVal {
					t.Errorf("value %d out of bounds [%d, %d]", v, tt.minVal, tt.maxVal)
				}
			}
		})
	}
}

func TestGenerateValue_Range_Integer_ExceedsBounds(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "integer", Generator: schema.GeneratorConfig{
				Method: "range", Min: int64(math.MinInt32 - 1), Max: int64(math.MaxInt32),
			}},
		},
	}
	if err := ParseSchemaAST(tbl); err == nil {
		t.Error("expected error for exceeding int32 min bounds")
	}
	tbl.Columns[0].Generator.Min = float64(0)
	tbl.Columns[0].Generator.Max = float64(math.MaxInt32 + 1)
	if err := ParseSchemaAST(tbl); err == nil {
		t.Error("expected error for exceeding int32 max bounds")
	}
}

func TestParseBoundInt64_Validation(t *testing.T) {
	tests := []struct {
		val     any
		want    int64
		wantErr bool
	}{
		{"9223372036854775807", math.MaxInt64, false},
		{"-9223372036854775808", math.MinInt64, false},
		{"9223372036854775808", 0, true},
		{"-9223372036854775809", 0, true},
		{"not-a-number", 0, true},
		{"123.45", 0, true},
		{float64(9223372036854775808.0), 0, true},
		{json.Number("9223372036854775808"), 0, true},
		{json.Number("9223372036854775807"), math.MaxInt64, false},
	}
	for _, tt := range tests {
		got, err := parseBoundInt64(tt.val)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseBoundInt64(%v) error = %v, wantErr %v", tt.val, err, tt.wantErr)
		} else if !tt.wantErr && got != tt.want {
			t.Errorf("parseBoundInt64(%v) = %v, want %v", tt.val, got, tt.want)
		}
	}
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "bigint", Generator: schema.GeneratorConfig{
				Method: "range", Min: "0", Max: "9223372036854775808",
			}},
		},
	}
	if err := ParseSchemaAST(tbl); err == nil {
		t.Error("expected error for bigint range bound exceeding int64 max")
	}
}

func TestParseBoundFloat64_NonFiniteRejection(t *testing.T) {
	nonFinite := []any{"NaN", "Inf", "-Inf", "+Inf", math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, val := range nonFinite {
		if _, err := parseBoundFloat64(val); err == nil {
			t.Errorf("parseBoundFloat64(%v) expected error, got nil", val)
		}
	}
}

func TestParseSchemaAST_UnsupportedRange(t *testing.T) {
	unsupported := []struct {
		name    string
		colType string
		min     any
		max     any
	}{
		{"boolean", "boolean", 1, 10},
		{"json_array", "json_array", 1, 10},
		{"json_object", "json_object", 1, 10},
		{"string_non_numeric", "string", "apple", "zebra"},
	}
	for _, tt := range unsupported {
		t.Run(tt.name, func(t *testing.T) {
			tbl := &schema.Table{
				Columns: []schema.Column{
					{Name: "bad_" + tt.name, Type: tt.colType, Generator: schema.GeneratorConfig{Method: "range", Min: tt.min, Max: tt.max}},
				},
			}
			if err := ParseSchemaAST(tbl); err == nil {
				t.Fatalf("expected error parsing schema AST with unsupported range %s", tt.name)
			}
			if _, err := NewGenerator(tbl, 1, 0, 42, nil); err == nil {
				t.Fatalf("expected error creating generator with unsupported range %s", tt.name)
			}
		})
	}
}

func verifyStringDateRange(t *testing.T, gen *Generator, wantLength int, wantLayout string) {
	for range 100 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		v := row[0].(string)
		if len(v) != wantLength {
			t.Errorf("expected string length %d for layout %s, got %d (%s)", wantLength, wantLayout, len(v), v)
		}
		if _, err := time.Parse(wantLayout, v); err != nil {
			t.Errorf("generated string %q does not match layout %q", v, wantLayout)
		}
	}
}

func TestGenerateValue_Range_StringDates(t *testing.T) {
	tests := []struct {
		name       string
		min        string
		max        string
		wantLength int
		wantLayout string
	}{
		{"date_only", "2024-01-01", "2024-12-31", 10, time.DateOnly},
		{"datetime", "2024-01-01 10:00:00", "2024-12-31 23:59:59", 19, time.DateTime},
		{"rfc3339", "2024-01-01T10:00:00Z", "2024-12-31T23:59:59Z", 20, time.RFC3339},
		{"mixed_formats", "2024-01-01", "2024-12-31T23:59:59Z", 20, time.RFC3339}, // Uses most precise format
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := setupSingleCol(t, schema.Column{
				Name: "date_col", Type: "string", Generator: schema.GeneratorConfig{
					Method: "range",
					Min:    tt.min,
					Max:    tt.max,
				},
			}, 100)
			verifyStringDateRange(t, gen, tt.wantLength, tt.wantLayout)
		})
	}
}

func TestGenerateValue_Range_StringNumeric(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "int_str", Type: "string", Generator: schema.GeneratorConfig{
				Method: "range",
				Min:    "1",
				Max:    "9",
			}},
			{Name: "float_str", Type: "string", Generator: schema.GeneratorConfig{
				Method: "range",
				Min:    "1.5",
				Max:    "5.5",
			}},
			{Name: "bigint_str", Type: "string", Generator: schema.GeneratorConfig{
				Method: "range",
				Min:    "10000000000",
				Max:    "99999999999",
			}},
		},
	}
	gen := setupGenerator(t, tbl, 100)
	for range 100 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		intStr := row[0].(string)
		if len(intStr) != 1 {
			t.Errorf("expected string length 1 for int_str, got %d (%s)", len(intStr), intStr)
		}
		floatStr := row[1].(string)
		if len(floatStr) < 3 {
			t.Errorf("expected float string representation for float_str, got %s", floatStr)
		}
		bigintStr := row[2].(string)
		if len(bigintStr) != 11 {
			t.Errorf("expected string length 11 for bigint_str, got %d (%s)", len(bigintStr), bigintStr)
		}
	}
}

func testTransform(t *testing.T, transform, val, want string) {
	t.Helper()
	gen := setupSingleCol(t, schema.Column{Name: "col", Type: "string", Generator: schema.GeneratorConfig{
		Method:    "constant",
		Value:     val,
		Transform: transform,
	}}, 1)
	row, err := gen.NextRow()
	if err != nil {
		t.Fatal(err)
	}
	if row[0] != want {
		t.Errorf("transform=%s got %v, want %s", transform, row[0], want)
	}
}

func TestGenerateValue_Transform_Upper(t *testing.T) {
	testTransform(t, "upper", "hello", "HELLO")
}

func TestGenerateValue_Transform_SHA256(t *testing.T) {
	testTransform(t, "sha256", "test", "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08")
}

func TestGenerateValue_MockGenerators(t *testing.T) {
	methods := []string{
		"first_name", "last_name", "city_name", "state_code", "company_name",
		"zip5_code", "phone_number", "street_address", "email_address",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			gen := setupSingleCol(t, schema.Column{Name: "val", Type: "string", Generator: schema.GeneratorConfig{Method: method}}, 1)
			row, err := gen.NextRow()
			if err != nil {
				t.Fatal(err)
			}
			if row[0] == nil || row[0] == "" {
				t.Errorf("%s returned empty value", method)
			}
		})
	}
}

func TestGenerateValue_MaxLength(t *testing.T) {
	fitLen := 50
	truncLen := 2
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "fits", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name", MaxLength: &fitLen}},
			{Name: "truncs", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name", MaxLength: &truncLen}},
		},
	}
	gen := setupGenerator(t, tbl, 10)
	for range 10 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := row[0].(string); ok && len(s) > 50 {
			t.Errorf("fits expected <= 50, got %d", len(s))
		}
		if s, ok := row[1].(string); ok && len(s) > 2 {
			t.Errorf("truncs expected <= 2, got %d", len(s))
		}
	}
}

func TestGenerateValue_Regex(t *testing.T) {
	gen := setupSingleCol(t, schema.Column{Name: "code", Type: "string", Generator: schema.GeneratorConfig{
		Method: "regex",
		Value:  "[A-Z]{3}",
	}}, 10)
	for range 10 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		s, ok := row[0].(string)
		if !ok || len(s) != 3 {
			t.Errorf("regex [A-Z]{3} produced %v (len %d)", row[0], len(s))
		}
	}
}

func TestGenerateValue_FallbackTypes(t *testing.T) {
	types := []string{"string", "integer", "bigint", "decimal", "boolean", "date", "datetime", "json_array", "json_object"}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			gen := setupSingleCol(t, schema.Column{Name: "val", Type: typ}, 1)
			row, err := gen.NextRow()
			if err != nil {
				t.Fatal(err)
			}
			if row[0] == nil {
				t.Errorf("fallback for type %s returned nil", typ)
			}
		})
	}
}

func TestGenerateValue_Deterministic(t *testing.T) {
	col := schema.Column{Name: "val", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name"}}
	gen1 := setupSingleCol(t, col, 5)
	gen2 := setupSingleCol(t, col, 5)
	for range 5 {
		r1, err := gen1.NextRow()
		if err != nil {
			t.Fatalf("NextRow error: %v", err)
		}
		r2, err := gen2.NextRow()
		if err != nil {
			t.Fatalf("NextRow error: %v", err)
		}
		if r1[0] != r2[0] {
			t.Errorf("same seed should produce same output: %v != %v", r1[0], r2[0])
		}
	}
}

func testCompileRegex(t *testing.T, pattern string, validate func(t *testing.T, res string)) {
	t.Helper()
	fn := CompileRegex(pattern)
	if fn == nil {
		t.Fatalf("CompileRegex returned nil for %s", pattern)
	}
	validate(t, fn(gofakeit.New(42)))
}

func testCompileRegexExact(t *testing.T, pattern, want string) {
	t.Helper()
	testCompileRegex(t, pattern, func(t *testing.T, res string) {
		if res != want {
			t.Errorf("CompileRegex(%s) = %s, want %s", pattern, res, want)
		}
	})
}

func TestCompileRegex_FixedLiteral(t *testing.T) {
	testCompileRegexExact(t, "ABC", "ABC")
}

func TestCompileRegex_CharClass(t *testing.T) {
	testCompileRegex(t, "[0-9]{5}", func(t *testing.T, res string) {
		if len(res) != 5 {
			t.Errorf("CompileRegex([0-9]{5}) length = %d, want 5", len(res))
		}
		for _, c := range res {
			if c < '0' || c > '9' {
				t.Errorf("CompileRegex([0-9]{5}) produced non-digit char: %c", c)
			}
		}
	})
}

func TestCompileRegex_Mixed(t *testing.T) {
	testCompileRegex(t, "[A-Z]{3}-[0-9]{4}", func(t *testing.T, res string) {
		if len(res) != 8 || res[3] != '-' {
			t.Errorf("CompileRegex([A-Z]{3}-[0-9]{4}) = %s, expected XXX-YYYY format", res)
		}
	})
}

func TestCompileRegex_VariableQuantifier(t *testing.T) {
	fn := CompileRegex("[a-z]{2,5}")
	if fn == nil {
		t.Fatal("CompileRegex returned nil for variable quantifier")
	}
	faker := gofakeit.New(42)
	for range 20 {
		res := fn(faker)
		if len(res) < 2 || len(res) > 5 {
			t.Errorf("CompileRegex([a-z]{2,5}) length = %d, want 2-5", len(res))
		}
	}
}

func TestCompileRegex_UnsupportedFallback(t *testing.T) {
	unsupported := []string{"(a|b)", "a+", "a*", "a?", ".", "^abc$", "a{-1}", "a{5,2}", "a{-2,5}", "a{2,-5}"}
	for _, pattern := range unsupported {
		t.Run(pattern, func(t *testing.T) {
			if CompileRegex(pattern) != nil {
				t.Errorf("CompileRegex(%s) should return nil for unsupported pattern", pattern)
			}
		})
	}
}

func TestParseSchemaAST_InvalidRegex(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "bad_regex", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: "[A-Z"}},
		},
	}
	if err := ParseSchemaAST(tbl); err == nil {
		t.Error("expected error for unclosed character class regex in ParseSchemaAST")
	}
}

func TestCompileRegex_Escape(t *testing.T) {
	testCompileRegexExact(t, `\-`, "-")
}

func TestCompileRegex_FastPathFallback(t *testing.T) {
	for _, pattern := range []string{`\d{3}`, `[^A]`, `[\-]`} {
		t.Run(pattern, func(t *testing.T) {
			if CompileRegex(pattern) != nil {
				t.Errorf("CompileRegex(%s) should return nil (fast path rejected, falling back to full regex engine)", pattern)
			}
		})
	}
}

func testHealthcareIdentifier(t *testing.T, method string, validate func(t *testing.T, val string)) {
	t.Helper()
	gen := setupSingleCol(t, schema.Column{Name: method, Type: "string", Generator: schema.GeneratorConfig{Method: method}}, 100)
	seen := make(map[string]bool)
	for range 100 {
		row, err := gen.NextRow()
		if err != nil {
			t.Fatal(err)
		}
		val := row[0].(string)
		validate(t, val)
		seen[val] = true
	}
	if len(seen) < 90 {
		t.Errorf("%s uniqueness too low: only %d unique out of 100", method, len(seen))
	}
}

func TestGenerateValue_SSN(t *testing.T) {
	testHealthcareIdentifier(t, "ssn", func(t *testing.T, ssn string) {
		if len(ssn) != 11 || ssn[3] != '-' || ssn[6] != '-' {
			t.Fatalf("SSN format invalid: %s (expected XXX-XX-XXXX)", ssn)
		}
		if area := ssn[0:3]; area < "900" || area > "999" {
			t.Errorf("SSN area must be 900-999 (invalid range), got %s", area)
		}
		if ssn[4:6] == "00" {
			t.Errorf("SSN group must not be 00, got %s", ssn)
		}
		if ssn[7:11] == "0000" {
			t.Errorf("SSN serial must not be 0000, got %s", ssn)
		}
	})
}

func runBenchmarkNextRow(b *testing.B, gen *Generator) {
	b.Helper()
	b.Cleanup(func() {
		if err := gen.Close(); err != nil {
			b.Errorf(errCloseGen, err)
		}
	})
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := gen.NextRow(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNextRow_All(b *testing.B) {
	maxLen := 20
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "integer", Generator: schema.GeneratorConfig{Method: "row_number"}},
			{Name: "c2", Type: "bigint", Generator: schema.GeneratorConfig{Method: "range", Min: 10000000000, Max: 99999999999}},
			{Name: "c3", Type: "datetime", Generator: schema.GeneratorConfig{Method: ""}},
			{Name: "c4", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "2020-01-01", Max: "2024-12-31"}},
			{Name: "c5", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: "[0-9]{3}-[A-Z]{3}"}},
			{Name: "c6", Type: "string", Generator: schema.GeneratorConfig{Method: "ssn"}},
			{Name: "c9", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name"}},
			{Name: "c10", Type: "string", Generator: schema.GeneratorConfig{Method: "company_name", MaxLength: &maxLen}},
			{Name: "c11", Type: "decimal", Generator: schema.GeneratorConfig{Method: "range", Min: 10.0, Max: 100.0}},
			{Name: "c12", Type: "string", Generator: schema.GeneratorConfig{Method: "choices", Choices: []any{"A", "B", "C", "D", "E"}, Weights: []int{10, 20, 30, 20, 20}}},
			{Name: "c13", Type: "boolean", Generator: schema.GeneratorConfig{Method: "choices", Choices: []any{true, false}, Weights: []int{80, 20}}},
			{Name: "c14", Type: "json_object", Generator: schema.GeneratorConfig{Method: ""}},
			{Name: "c15", Type: "json_array", Generator: schema.GeneratorConfig{Method: ""}},
		},
	}
	if err := ParseSchemaAST(tbl); err != nil {
		b.Fatalf(errParseAST, err)
	}
	gen, err := NewGenerator(tbl, b.N, 0, 42, nil)
	if err != nil {
		b.Fatal(err)
	}
	runBenchmarkNextRow(b, gen)
}

type mockStreamer struct{}

func (m *mockStreamer) NextRow(dst map[string]any) error      { dst["code"] = "ABC"; return nil }
func (m *mockStreamer) Reset() error                          { return nil }
func (m *mockStreamer) Close() error                          { return nil }
func (m *mockStreamer) SeekRow(offset int64, wrap bool) error { return nil }
func (m *mockStreamer) TotalRows() (int64, error)             { return 100, nil }

func BenchmarkNextRow_Lookup(b *testing.B) {
	tbl := &schema.Table{
		Sources: map[string]schema.SourceConfig{
			"mock": {Selection: "random"},
		},
		Columns: []schema.Column{
			{Name: "lookup_col", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@mock.code"}},
		},
	}
	if err := ParseSchemaAST(tbl); err != nil {
		b.Fatalf(errParseAST, err)
	}
	sm := &SourceManager{
		Configs: tbl.Sources,
		RandomStreamers: map[string]RowStreamer{
			"mock": &mockStreamer{},
		},
		RandomTotals: map[string]int64{
			"mock": 100,
		},
	}
	gen, err := NewGenerator(tbl, b.N, 0, 42, sm)
	if err != nil {
		b.Fatal(err)
	}
	runBenchmarkNextRow(b, gen)
}
