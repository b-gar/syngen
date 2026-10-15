package config

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestLoad_FormatNormalization(t *testing.T) {
	t.Setenv("TABLE_NAME", "users")
	t.Setenv("OUTPUT_PREFIX", "out")
	t.Setenv("N_ROWS", "10")
	t.Setenv("FORMAT", "CSV")
	cfg := Load()
	if cfg.Format != "csv" {
		t.Errorf("expected format to be normalized to 'csv', got %s", cfg.Format)
	}
}

func TestLoad_Partition(t *testing.T) {
	t.Setenv("TABLE_NAME", "users")
	t.Setenv("OUTPUT_PREFIX", "out")
	t.Setenv("N_ROWS", "10")
	t.Setenv("FORMAT", "parquet")
	t.Setenv("PARTITION", "5")
	cfg := Load()
	if cfg.Partition != 5 {
		t.Errorf("expected partition 5, got %d", cfg.Partition)
	}
}

func TestLoad_PartitionDefault(t *testing.T) {
	t.Setenv("TABLE_NAME", "users")
	t.Setenv("OUTPUT_PREFIX", "out")
	t.Setenv("N_ROWS", "10")
	t.Setenv("FORMAT", "parquet")
	t.Setenv("PARTITION", "")
	cfg := Load()
	if cfg.Partition != 0 {
		t.Errorf("expected default partition 0, got %d", cfg.Partition)
	}
}

func TestLoad_Seed(t *testing.T) {
	t.Setenv("TABLE_NAME", "users")
	t.Setenv("OUTPUT_PREFIX", "out")
	t.Setenv("N_ROWS", "10")
	t.Setenv("FORMAT", "parquet")
	t.Setenv("SEED", "12345")
	cfg := Load()
	if cfg.Seed != 12345 {
		t.Errorf("expected seed 12345, got %d", cfg.Seed)
	}
}

func TestLoad_SeedDefault(t *testing.T) {
	t.Setenv("TABLE_NAME", "users")
	t.Setenv("OUTPUT_PREFIX", "out")
	t.Setenv("N_ROWS", "10")
	t.Setenv("FORMAT", "parquet")
	t.Setenv("SEED", "")
	cfg := Load()
	if cfg.Seed != 0 {
		t.Errorf("expected default seed 0, got %d", cfg.Seed)
	}
}

func TestLoad_FatalHelper(t *testing.T) {
	if os.Getenv("TEST_FATAL_HELPER") != "1" {
		return
	}
	Load()
}

func runFatalTest(t *testing.T, env map[string]string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoad_FatalHelper$")
	cmdEnv := append(os.Environ(), "TEST_FATAL_HELPER=1")
	baseEnv := map[string]string{
		"TABLE_NAME":    "users",
		"OUTPUT_PREFIX": "out",
		"N_ROWS":        "10",
		"FORMAT":        "parquet",
		"PARTITION":     "0",
		"SEED":          "0",
	}
	for k, v := range env {
		baseEnv[k] = v
	}
	for k, v := range baseEnv {
		cmdEnv = append(cmdEnv, k+"="+v)
	}
	cmd.Env = cmdEnv
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected command to exit with fatal error, but it succeeded with env: %v", env)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit code, got %v", err)
	}
}

func TestLoad_FatalValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"zero_rows", map[string]string{"N_ROWS": "0"}},
		{"negative_rows", map[string]string{"N_ROWS": "-5"}},
		{"malformed_rows", map[string]string{"N_ROWS": "abc"}},
		{"negative_partition", map[string]string{"PARTITION": "-1"}},
		{"malformed_partition", map[string]string{"PARTITION": "xyz"}},
		{"unsupported_format", map[string]string{"FORMAT": "json"}},
		{"malformed_seed", map[string]string{"SEED": "invalid"}},
		{"missing_table_name", map[string]string{"TABLE_NAME": ""}},
		{"missing_output_prefix", map[string]string{"OUTPUT_PREFIX": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runFatalTest(t, tt.env)
		})
	}
}
