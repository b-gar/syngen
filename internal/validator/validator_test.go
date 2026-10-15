package validator

import (
	"testing"

	"github.com/b-gar/syngen/internal/schema"
)

func TestValidate_ValidSchema(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "id", Type: "integer", Generator: schema.GeneratorConfig{Method: "row_number"}},
			{Name: "name", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) > 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_DuplicateColumnName(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "string"},
			{Name: "c1", Type: "integer"},
		},
	}
	errs := Validate(tbl)
	if len(errs) != 1 {
		t.Errorf("expected 1 error for duplicate column name, got %d", len(errs))
	}
}

func TestValidate_MaxLength_Invalid(t *testing.T) {
	zeroLen := 0
	negLen := -5
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name", MaxLength: &zeroLen}},
			{Name: "c2", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name", MaxLength: &negLen}},
		},
	}
	errs := Validate(tbl)
	if len(errs) != 2 {
		t.Errorf("expected 2 errors for invalid max_length, got %d", len(errs))
	}
}

func TestValidate_MaxLength_UnsupportedMethod(t *testing.T) {
	len10 := 10
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: "[A-Z]", MaxLength: &len10}},
		},
	}
	errs := Validate(tbl)
	if len(errs) != 1 {
		t.Errorf("expected 1 error for unsupported max_length on regex method, got %d", len(errs))
	}
}

func TestValidate_NullPercentage_Invalid(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "string", NullPercentage: -1},
			{Name: "c2", Type: "string", NullPercentage: 101},
		},
	}
	errs := Validate(tbl)
	if len(errs) != 2 {
		t.Errorf("expected 2 errors for invalid null_percentage, got %d", len(errs))
	}
}

func TestValidate_MissingName(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "", Type: "string"},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for missing column name")
	}
}

func TestValidate_InvalidType(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "varchar"},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for invalid type 'varchar'")
	}
}

func TestValidate_InvalidMethod(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "invalid_method"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for invalid method 'invalid_method'")
	}
}

func TestValidate_RangeMissingBounds(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "integer", Generator: schema.GeneratorConfig{Method: "range", Min: 1}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for range missing max")
	}
}

func TestValidate_RangeUnsupportedType(t *testing.T) {
	unsupported := []string{"boolean", "json_array", "json_object"}
	for _, typ := range unsupported {
		t.Run(typ, func(t *testing.T) {
			tbl := &schema.Table{
				Columns: []schema.Column{
					{Name: "test", Type: typ, Generator: schema.GeneratorConfig{Method: "range", Min: 1, Max: 10}},
				},
			}
			errs := Validate(tbl)
			if len(errs) == 0 {
				t.Errorf("expected error for range method on unsupported type %s", typ)
			}
		})
	}
}

func TestValidate_ChoicesEmpty(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "choices", Choices: []any{}}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for empty choices")
	}
}

func TestValidate_WeightsMismatch(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{
				Method:  "choices",
				Choices: []any{"A", "B"},
				Weights: []int{1, 2, 3},
			}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for weights/choices length mismatch")
	}
}

func TestValidate_ConstantMissingValue(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "constant"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for constant missing value")
	}
}

func TestValidate_LookupInvalidFormat(t *testing.T) {
	tbl := &schema.Table{
		Sources: map[string]schema.SourceConfig{"src": {Seed: "test.csv"}},
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@badformat"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for invalid lookup format (missing dot)")
	}
}

func TestValidate_LookupUndefinedSource(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@missing.col"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for undefined source")
	}
}

func TestValidate_UnusedSource(t *testing.T) {
	tbl := &schema.Table{
		Sources: map[string]schema.SourceConfig{
			"used":   {Seed: "a.csv"},
			"unused": {Seed: "b.csv"},
		},
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@used.col"}},
		},
	}
	errs := Validate(tbl)
	found := false
	for _, e := range errs {
		if e.Error() == "source 'unused' is defined in the schema but never referenced by any column" {
			found = true
		}
	}
	if !found {
		t.Error("expected error for unused source 'unused'")
	}
}

func TestValidate_DecimalPartialPrecision(t *testing.T) {
	precision := 10
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "decimal", Precision: &precision},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for decimal with precision but no scale")
	}
}

func TestValidate_RegexMissingValue(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "regex"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for regex missing value")
	}
}

func TestValidate_RegexNonStringValue(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: 123}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for regex with non-string value")
	}
}

func TestValidate_AllValidTypes(t *testing.T) {
	types := []string{"string", "integer", "bigint", "decimal", "boolean", "date", "datetime", "json_array", "json_object"}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			tbl := &schema.Table{
				Columns: []schema.Column{
					{Name: "test", Type: typ},
				},
			}
			errs := Validate(tbl)
			if len(errs) > 0 {
				t.Errorf("type %s should be valid, got errors: %v", typ, errs)
			}
		})
	}
}

func TestValidate_LookupValidReference(t *testing.T) {
	tbl := &schema.Table{
		Sources: map[string]schema.SourceConfig{
			"test": {Table: "test", Selection: "random"},
		},
		Columns: []schema.Column{
			{Name: "col1", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@test.col1"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) > 0 {
		t.Errorf("valid lookup should pass, got errors: %v", errs)
	}
}

func TestValidate_WeightsSumInvalid(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{
				Method:  "choices",
				Choices: []any{"A", "B"},
				Weights: []int{50, 49},
			}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for weights not summing to 100")
	}
}

func TestValidate_WeightsNegative(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "test", Type: "string", Generator: schema.GeneratorConfig{
				Method:  "choices",
				Choices: []any{"A", "B"},
				Weights: []int{-10, 110},
			}},
		},
	}
	errs := Validate(tbl)
	if len(errs) == 0 {
		t.Error("expected error for negative weights")
	}
}

func TestValidate_DecimalPrecisionScaleBounds(t *testing.T) {
	badPrec := 0
	badScale := 0
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "bad_prec", Type: "decimal", Precision: &badPrec, Scale: &badScale},
		},
	}
	if errs := Validate(tbl); len(errs) == 0 {
		t.Error("expected error for zero precision")
	}
	p := 40
	s := 2
	tbl.Columns[0] = schema.Column{Name: "too_large_prec", Type: "decimal", Precision: &p, Scale: &s}
	if errs := Validate(tbl); len(errs) == 0 {
		t.Error("expected error for precision > 38")
	}
	p2 := 5
	s2 := 6
	tbl.Columns[0] = schema.Column{Name: "scale_gt_prec", Type: "decimal", Precision: &p2, Scale: &s2}
	if errs := Validate(tbl); len(errs) == 0 {
		t.Error("expected error for scale > precision")
	}
	p3 := 38
	s3 := 38
	tbl.Columns[0] = schema.Column{Name: "valid_decimal_38_38", Type: "decimal", Precision: &p3, Scale: &s3}
	if errs := Validate(tbl); len(errs) > 0 {
		t.Errorf("expected precision 38 and scale 38 to be valid, got: %v", errs)
	}
	pEqual := 10
	sEqual := 10
	tbl.Columns[0] = schema.Column{Name: "valid_equal", Type: "decimal", Precision: &pEqual, Scale: &sEqual}
	if errs := Validate(tbl); len(errs) > 0 {
		t.Errorf("expected scale equal to precision to be valid, got: %v", errs)
	}
}

func TestValidate_RegexPatternSyntax(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "valid_regex", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: "[A-Z]{3}[0-9]{2}"}},
			{Name: "invalid_regex", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: "[A-Z"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error for unclosed character class regex, got %d: %v", len(errs), errs)
	}
}

func TestValidate_ASTTypeValidation(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "empty_regex", Type: "string", Generator: schema.GeneratorConfig{Method: "regex", Value: ""}},
			{Name: "nonnumeric_str_range", Type: "string", Generator: schema.GeneratorConfig{Method: "range", Min: "abc", Max: "xyz"}},
			{Name: "int_exceeds_int32", Type: "integer", Generator: schema.GeneratorConfig{Method: "range", Min: 1, Max: int64(3000000000)}},
		},
	}
	errs := Validate(tbl)
	if len(errs) < 3 {
		t.Errorf("expected at least 3 errors for invalid AST/type values, got %d: %v", len(errs), errs)
	}
}

func TestValidate_DynamicDateReferences(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "@nonexistent", Max: "0d"}},
			{Name: "c2", Type: "integer"},
			{Name: "c3", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "@c2", Max: "0d"}},
			{Name: "c4", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "@c5", Max: "0d"}},
			{Name: "c5", Type: "date", Generator: schema.GeneratorConfig{Method: "range", Min: "2020-01-01", Max: "2020-01-02"}},
		},
	}
	errs := Validate(tbl)
	if len(errs) != 3 {
		t.Fatalf("expected 3 dynamic date reference errors, got %d: %v", len(errs), errs)
	}
}

func TestValidate_InvalidTransform(t *testing.T) {
	tbl := &schema.Table{
		Columns: []schema.Column{
			{Name: "c1", Type: "string", Generator: schema.GeneratorConfig{Method: "first_name", Transform: "md5"}},
		},
	}
	if errs := Validate(tbl); len(errs) != 1 {
		t.Fatalf("expected 1 error for invalid transform, got %d: %v", len(errs), errs)
	}
}

func TestValidate_InvalidSourceConfig(t *testing.T) {
	tbl := &schema.Table{
		Sources: map[string]schema.SourceConfig{
			"missing_target": {Selection: "next"},
			"bad_selection":  {Seed: "a.csv", Selection: "shuffle"},
		},
		Columns: []schema.Column{
			{Name: "c1", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@missing_target.id"}},
			{Name: "c2", Type: "string", Generator: schema.GeneratorConfig{Method: "lookup", Value: "@bad_selection.id"}},
		},
	}
	if errs := Validate(tbl); len(errs) != 2 {
		t.Fatalf("expected 2 source config errors, got %d: %v", len(errs), errs)
	}
}
