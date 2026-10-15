package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/b-gar/syngen/internal/schema"
	"github.com/b-gar/syngen/internal/validator"
)

func main() {
	schemasDir := "schemas"
	if len(os.Args) > 1 {
		schemasDir = os.Args[1]
	}
	fmt.Printf("Validating schemas in '%s'...\n\n", schemasDir)
	totalFiles := 0
	failedFiles := 0
	totalErrors := 0
	err := filepath.Walk(schemasDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			totalFiles++
			tbl, parseErr := schema.ParseFile(path)
			if parseErr != nil {
				fmt.Printf("❌ [FAIL] %s\n", path)
				fmt.Printf("   - JSON Parse Error: %v\n\n", parseErr)
				failedFiles++
				totalErrors++
				return nil
			}
			validationErrs := validator.Validate(tbl)
			if len(validationErrs) > 0 {
				fmt.Printf("❌ [FAIL] %s\n", path)
				for _, vErr := range validationErrs {
					fmt.Printf("   - %v\n", vErr)
					totalErrors++
				}
				fmt.Println()
				failedFiles++
			} else {
				fmt.Printf("✅ [PASS] %s\n", path)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("Failed to traverse schemas directory: %v", err)
	}
	fmt.Printf("\n--- Validation Summary ---\n")
	fmt.Printf("Files checked: %d\n", totalFiles)
	fmt.Printf("Files failed:  %d\n", failedFiles)
	fmt.Printf("Total errors:  %d\n", totalErrors)
	if failedFiles > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}
