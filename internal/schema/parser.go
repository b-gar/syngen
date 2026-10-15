package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// ParseFile reads a JSON file and parses it into a Table structure.
func ParseFile(path string) (*Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var table Table
	if err := decoder.Decode(&table); err != nil {
		return nil, err
	}
	var dummy any
	if err := decoder.Decode(&dummy); !errors.Is(err, io.EOF) {
		return nil, errors.New("extra data after top-level value")
	}
	return &table, nil
}
