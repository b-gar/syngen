package validator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/b-gar/syngen/internal/generator"
	"github.com/b-gar/syngen/internal/schema"
)

var validTypes = []string{
	"string", "integer", "bigint", "decimal",
	"boolean", "date", "datetime",
	"json_array", "json_object",
}

var validMethods = []string{
	"row_number", "constant", "lookup",
	"range", "choices", "regex", "",
}

var validMockMethods = []string{
	"first_name", "last_name", "ssn",
	"city_name", "state_code", "company_name",
	"zip5_code", "phone_number", "street_address",
	"email_address",
}

var validTransforms = []string{
	"upper", "lower", "sha256", "",
}

var validSelections = []string{
	"next", "wrap", "random", "",
}

func Validate(tbl *schema.Table) []error {
	var errors []error
	usedSources := make(map[string]bool)
	seenColumns := make(map[string]bool)
	for i, col := range tbl.Columns {
		errors = append(errors, validateColumnBasics(col, seenColumns, i)...)
		errors = append(errors, validateGenerator(col)...)
		errors = append(errors, validateSourceReferences(tbl, col, usedSources)...)
	}
	if err := generator.ParseSchemaAST(tbl); err != nil {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			errors = append(errors, joined.Unwrap()...)
		} else {
			errors = append(errors, err)
		}
	}
	errors = append(errors, validateSources(tbl, usedSources)...)
	return errors
}

func validateColumnBasics(col schema.Column, seenColumns map[string]bool, index int) []error {
	var errs []error
	if col.Name == "" {
		errs = append(errs, fmt.Errorf("column at index %d is missing 'name'", index))
	} else {
		if seenColumns[col.Name] {
			errs = append(errs, fmt.Errorf("duplicate column name '%s' detected", col.Name))
		}
		seenColumns[col.Name] = true
	}
	if !slices.Contains(validTypes, col.Type) {
		errs = append(errs, fmt.Errorf("column '%s' has invalid type '%s'", col.Name, col.Type))
	}
	if (col.Precision == nil) != (col.Scale == nil) {
		errs = append(errs, fmt.Errorf("column '%s' must have both 'precision' and 'scale' if one is defined", col.Name))
	} else if col.Precision != nil && col.Scale != nil {
		if *col.Precision <= 0 || *col.Precision > 38 {
			errs = append(errs, fmt.Errorf("column '%s' precision must be between 1 and 38, got %d", col.Name, *col.Precision))
		}
		if *col.Scale < 0 || *col.Scale > *col.Precision {
			errs = append(errs, fmt.Errorf("column '%s' scale must be between 0 and precision (%d), got %d", col.Name, *col.Precision, *col.Scale))
		}
	}
	if col.NullPercentage < 0 || col.NullPercentage > 100 {
		errs = append(errs, fmt.Errorf("column '%s' has invalid null_percentage %d (must be between 0 and 100)", col.Name, col.NullPercentage))
	}
	return errs
}

func validateGenerator(col schema.Column) []error {
	var errs []error
	gen := col.Generator
	if !slices.Contains(validMethods, gen.Method) && !slices.Contains(validMockMethods, gen.Method) {
		errs = append(errs, fmt.Errorf("column '%s' has invalid generator method '%s'", col.Name, gen.Method))
	}
	if !slices.Contains(validTransforms, gen.Transform) {
		errs = append(errs, fmt.Errorf("column '%s' has invalid transform '%s'", col.Name, gen.Transform))
	}
	if gen.MaxLength != nil {
		if !slices.Contains(validMockMethods, gen.Method) {
			errs = append(errs, fmt.Errorf("column '%s' uses 'max_length' but method '%s' does not support it", col.Name, gen.Method))
		} else if *gen.MaxLength <= 0 {
			errs = append(errs, fmt.Errorf("column '%s' has invalid 'max_length' %d (must be > 0)", col.Name, *gen.MaxLength))
		}
	}
	errs = append(errs, validateMethodRequirements(col)...)
	return errs
}

func validateMethodRequirements(col schema.Column) []error {
	var errs []error
	gen := col.Generator
	switch gen.Method {
	case "range":
		if gen.Min == nil || gen.Max == nil {
			errs = append(errs, fmt.Errorf("column '%s' using 'range' method must specify both 'min' and 'max'", col.Name))
		}
		switch col.Type {
		case "integer", "bigint", "decimal", "date", "datetime", "string":
		default:
			errs = append(errs, fmt.Errorf("column '%s' of type '%s' does not support 'range' method", col.Name, col.Type))
		}
	case "choices":
		errs = append(errs, validateChoicesMethod(col, gen)...)
	case "regex":
		if gen.Value == nil {
			errs = append(errs, fmt.Errorf("column '%s' using 'regex' method must provide a 'value' string", col.Name))
		} else if _, ok := gen.Value.(string); !ok {
			errs = append(errs, fmt.Errorf("column '%s' using 'regex' method must have a string 'value'", col.Name))
		}
	case "constant":
		if gen.Value == nil {
			errs = append(errs, fmt.Errorf("column '%s' using 'constant' method must provide a 'value'", col.Name))
		}
	case "lookup":
		if gen.Value == nil {
			errs = append(errs, fmt.Errorf("column '%s' using '%s' method must provide a 'value' starting with '@'", col.Name, gen.Method))
		} else if s, ok := gen.Value.(string); !ok || !strings.HasPrefix(s, "@") {
			errs = append(errs, fmt.Errorf("column '%s' using '%s' method must provide a 'value' string starting with '@'", col.Name, gen.Method))
		}
	}
	return errs
}

func validateChoicesMethod(col schema.Column, gen schema.GeneratorConfig) []error {
	var errs []error
	if len(gen.Choices) == 0 {
		errs = append(errs, fmt.Errorf("column '%s' using 'choices' method must provide a non-empty 'choices' array", col.Name))
	}
	if len(gen.Weights) > 0 {
		if len(gen.Weights) != len(gen.Choices) {
			errs = append(errs, fmt.Errorf("column '%s' has %d choices but %d weights", col.Name, len(gen.Choices), len(gen.Weights)))
		} else {
			sum := 0
			for _, w := range gen.Weights {
				if w < 0 {
					errs = append(errs, fmt.Errorf("column '%s' weight must be non-negative, got %d", col.Name, w))
				}
				sum += w
			}
			if sum != 100 {
				errs = append(errs, fmt.Errorf("column '%s' weights must sum to exactly 100, but got %d", col.Name, sum))
			}
		}
	}
	return errs
}

func validateSourceReferences(tbl *schema.Table, col schema.Column, usedSources map[string]bool) []error {
	var errs []error
	gen := col.Generator
	if gen.Value != nil {
		if s, ok := gen.Value.(string); ok && strings.HasPrefix(s, "@") {
			parts := strings.SplitN(strings.TrimPrefix(s, "@"), ".", 2)
			if len(parts) != 2 {
				errs = append(errs, fmt.Errorf("column '%s' has invalid lookup format '%s' (expected @source.column)", col.Name, s))
			} else {
				sourceName := parts[0]
				usedSources[sourceName] = true
				if tbl.Sources == nil {
					errs = append(errs, fmt.Errorf("column '%s' references source '%s', but no sources are defined in this schema", col.Name, sourceName))
				} else if _, exists := tbl.Sources[sourceName]; !exists {
					errs = append(errs, fmt.Errorf("column '%s' references undefined source '%s'", col.Name, sourceName))
				}
			}
		}
	}
	return errs
}

func validateSources(tbl *schema.Table, usedSources map[string]bool) []error {
	var errs []error
	if tbl.Sources != nil {
		for srcName, srcCfg := range tbl.Sources {
			if srcCfg.Table == "" && srcCfg.Seed == "" {
				errs = append(errs, fmt.Errorf("source '%s' must specify either 'table' or 'seed'", srcName))
			}
			if !slices.Contains(validSelections, srcCfg.Selection) {
				errs = append(errs, fmt.Errorf("source '%s' has invalid selection '%s'", srcName, srcCfg.Selection))
			}
			if !usedSources[srcName] {
				errs = append(errs, fmt.Errorf("source '%s' is defined in the schema but never referenced by any column", srcName))
			}
		}
	}
	return errs
}
