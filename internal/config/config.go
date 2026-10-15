package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

// Config holds the application configuration.
type Config struct {
	TableName    string
	OutputPrefix string
	Rows         int
	Format       string
	Partition    int
	Seed         int
}

// Load loads the configuration from environment variables, requiring explicit values.
func Load() *Config {
	cfg := &Config{
		TableName:    requireEnv("TABLE_NAME"),
		OutputPrefix: requireEnv("OUTPUT_PREFIX"),
		Rows:         requireEnvInt("N_ROWS"),
		Format:       requireEnv("FORMAT"),
		Partition:    optionalEnvInt("PARTITION", 0),
		Seed:         optionalEnvInt("SEED", 0),
	}
	if cfg.Rows <= 0 {
		log.Fatalf("Error: Environment variable N_ROWS must be greater than 0, got: %d", cfg.Rows)
	}
	if cfg.Partition < 0 {
		log.Fatalf("Error: Environment variable PARTITION must be non-negative, got: %d", cfg.Partition)
	}
	format := strings.ToLower(cfg.Format)
	if format != "parquet" && format != "csv" {
		log.Fatalf("Error: Environment variable FORMAT must be 'parquet' or 'csv', got: %s", cfg.Format)
	}
	cfg.Format = format
	return cfg
}

func requireEnv(key string) string {
	val, exists := os.LookupEnv(key)
	if !exists || val == "" {
		log.Fatalf("Error: Environment variable %s is required", key)
	}
	return val
}

func requireEnvInt(key string) int {
	strValue := requireEnv(key)
	val, err := strconv.Atoi(strValue)
	if err != nil {
		log.Fatalf("Error: Environment variable %s must be a valid integer, got: %s", key, strValue)
	}
	return val
}

func optionalEnvInt(key string, defaultVal int) int {
	strValue, exists := os.LookupEnv(key)
	if !exists || strValue == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(strValue)
	if err != nil {
		log.Fatalf("Error: Environment variable %s must be a valid integer, got: %s", key, strValue)
	}
	return val
}
