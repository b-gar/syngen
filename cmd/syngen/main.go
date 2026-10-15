package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/b-gar/syngen/internal/config"
	"github.com/b-gar/syngen/internal/generator"
	"github.com/b-gar/syngen/internal/schema"
	"github.com/b-gar/syngen/internal/writer"
)

func main() {
	cfg := config.Load()
	fmt.Printf("Starting Syngen\n")
	fmt.Printf("Table: %s\nOutput Prefix: %s\nFormat: %s\nRows: %d\nPartition: %d\n",
		cfg.TableName, cfg.OutputPrefix, cfg.Format, cfg.Rows, cfg.Partition)
	schemaFile, err := findSchemaFile(cfg.TableName)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nProcessing schema: %s\n", schemaFile)
	tbl, err := schema.ParseFile(schemaFile)
	if err != nil {
		log.Fatalf("Failed to parse %s: %v", schemaFile, err)
	}
	fmt.Printf("Parsed %d columns\n", len(tbl.Columns))
	outFile := fmt.Sprintf("%s%s_part_%d.%s", cfg.OutputPrefix, cfg.TableName, cfg.Partition, cfg.Format)
	if err := prepareOutputDir(outFile, cfg.OutputPrefix); err != nil {
		log.Fatalf("Failed to create output dir: %v", err)
	}
	if err := generator.ParseSchemaAST(tbl); err != nil {
		log.Fatalf("Failed to parse schema AST: %v", err)
	}
	reqCols := generator.FindRequiredColumns(tbl)
	sm, err := generator.NewSourceManager(tbl, cfg.OutputPrefix, cfg.Partition, cfg.Format, reqCols)
	if err != nil {
		log.Fatalf("Failed to initialize sources: %v", err)
	}
	defer sm.Close()
	start := time.Now()
	gen, err := generator.NewGenerator(tbl, cfg.Rows, cfg.Partition, int64(cfg.Seed), sm)
	if err != nil {
		log.Fatalf("Failed to initialize generator: %v", err)
	}
	defer func() {
		if err := gen.Close(); err != nil {
			log.Printf("error closing generator: %v", err)
		}
	}()
	if err := writeTableData(cfg.Format, outFile, tbl, cfg.Rows, gen); err != nil {
		log.Fatalf("Failed to write output: %v", err)
	}
	fmt.Printf("Generated %d rows to %s in %v\n", cfg.Rows, outFile, time.Since(start))
}

func findSchemaFile(tableName string) (string, error) {
	targetPath := filepath.Join("schemas", tableName+".json")
	if info, err := os.Stat(targetPath); err == nil && !info.IsDir() {
		return targetPath, nil
	}
	var matches []string
	targetName := filepath.Base(tableName) + ".json"
	err := filepath.Walk("schemas", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.EqualFold(info.Name(), targetName) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("error searching for schema: %w", err)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous table name '%s': matches %v. Please use a path-qualified table identifier (e.g. 'idr/%s')", tableName, matches, filepath.Base(tableName))
	}
	return "", fmt.Errorf("could not find schema for table '%s' in 'schemas/'", tableName)
}

func prepareOutputDir(outFile, outputPrefix string) error {
	if strings.HasPrefix(outputPrefix, "s3://") {
		return nil
	}
	return os.MkdirAll(filepath.Dir(outFile), 0o755)
}

func writeTableData(format, outFile string, tbl *schema.Table, rows int, gen *generator.Generator) error {
	if strings.EqualFold(format, "csv") {
		return writer.WriteCSV(outFile, tbl, rows, gen)
	}
	return writer.WriteParquet(outFile, tbl, rows, gen)
}
