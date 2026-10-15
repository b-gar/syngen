package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseFile_StrictDecoding(t *testing.T) {
	// Create a temporary JSON file with an unknown field
	content := `{
		"columns": [
			{
				"name": "id",
				"type": "integer",
				"invalid_key": "should fail"
			}
		]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(path)
	if err == nil {
		t.Error("expected error due to strict decoding of unknown field 'invalid_key', got nil")
	}
}

func TestParseFile_TrailingData(t *testing.T) {
	// Create a temporary JSON file with a valid object followed by trailing garbage
	content := `{
		"columns": [
			{
				"name": "id",
				"type": "integer"
			}
		]
	}
	SOME ACCIDENTAL GARBAGE`
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(path)
	if err == nil {
		t.Error("expected error due to trailing garbage data, got nil")
	}
}

func TestParseFile_BigintDecoding(t *testing.T) {
	content := `{
		"columns": [
			{
				"name": "id",
				"type": "bigint",
				"generator": {
					"method": "range",
					"min": 9223372036854775807,
					"max": 9223372036854775808
				}
			}
		]
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tbl, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile unexpectedly failed: %v", err)
	}
	if len(tbl.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(tbl.Columns))
	}
	minVal := tbl.Columns[0].Generator.Min
	maxVal := tbl.Columns[0].Generator.Max
	minNum, ok := minVal.(json.Number)
	if !ok {
		t.Fatalf("expected min to be json.Number, got %T", minVal)
	}
	if minNum.String() != "9223372036854775807" {
		t.Errorf("expected min string '9223372036854775807', got '%s'", minNum.String())
	}
	maxNum, ok := maxVal.(json.Number)
	if !ok {
		t.Fatalf("expected max to be json.Number, got %T", maxVal)
	}
	if maxNum.String() != "9223372036854775808" {
		t.Errorf("expected max string '9223372036854775808', got '%s'", maxNum.String())
	}
}
