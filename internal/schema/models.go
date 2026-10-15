package schema

import (
	"time"

	"github.com/brianvoe/gofakeit/v7"
)

// Table represents the full JSON schema definition for a file/table.
type Table struct {
	Sources map[string]SourceConfig `json:"sources,omitempty"`
	Columns []Column                `json:"columns"`
}

// SourceConfig holds the configuration for a seed file.
type SourceConfig struct {
	Type      string `json:"type,omitempty"`
	Table     string `json:"table,omitempty"`
	Seed      string `json:"seed,omitempty"`
	Selection string `json:"selection,omitempty"`
}

type Column struct {
	Name           string          `json:"name"`
	Type           string          `json:"type"`
	Precision      *int            `json:"precision,omitempty"`
	Scale          *int            `json:"scale,omitempty"`
	NullPercentage int             `json:"null_percentage,omitempty"`
	NullValue      any             `json:"null_value,omitempty"`
	Generator      GeneratorConfig `json:"generator,omitempty"`

	ParsedNullValue any `json:"-"`
}

// GeneratorConfig holds the rules for generating a specific column.
type GeneratorConfig struct {
	Method    string `json:"method,omitempty"`
	Value     any    `json:"value,omitempty"`
	Min       any    `json:"min,omitempty"`
	Max       any    `json:"max,omitempty"`
	Choices   []any  `json:"choices,omitempty"`
	Weights   []int  `json:"weights,omitempty"`
	Transform string `json:"transform,omitempty"`
	MaxLength *int   `json:"max_length,omitempty"`

	// AST Caching Fields (internal)
	ParsedMinDate           time.Time                          `json:"-"`
	ParsedMaxDate           time.Time                          `json:"-"`
	ParsedDateFormat        string                             `json:"-"`
	ParsedMinFloat          float64                            `json:"-"`
	ParsedMaxFloat          float64                            `json:"-"`
	ParsedMinInt            int                                `json:"-"`
	ParsedMaxInt            int                                `json:"-"`
	ParsedMinBigint         int64                              `json:"-"`
	ParsedMaxBigint         int64                              `json:"-"`
	ParsedChoices           []any                              `json:"-"`
	ParsedCumulativeWeights []int                              `json:"-"`
	TotalWeight             int                                `json:"-"`
	ParsedValue             any                                `json:"-"`
	IsRangeCached           bool                               `json:"-"`
	IsDateStringRange       bool                               `json:"-"`
	IsIntStringRange        bool                               `json:"-"`
	IsFloatStringRange      bool                               `json:"-"`
	ParsedPrecision         *int                               `json:"-"`
	ParsedScale             *int                               `json:"-"`
	ParsedScaleShift        float64                            `json:"-"`
	FastRegexFunc           func(faker *gofakeit.Faker) string `json:"-"`
	IsLookup                bool                               `json:"-"`
	ParsedLookupSource      string                             `json:"-"`
	ParsedLookupColumn      string                             `json:"-"`
	ParsedMinDynamicRef     string                             `json:"-"`
	ParsedMinDynamicSign    string                             `json:"-"`
	ParsedMinDynamicNum     int                                `json:"-"`
	ParsedMinDynamicUnit    string                             `json:"-"`
	ParsedMinDynamicIdx     int                                `json:"-"`
	ParsedMaxDynamicRef     string                             `json:"-"`
	ParsedMaxDynamicSign    string                             `json:"-"`
	ParsedMaxDynamicNum     int                                `json:"-"`
	ParsedMaxDynamicUnit    string                             `json:"-"`
	ParsedMaxDynamicIdx     int                                `json:"-"`
}
