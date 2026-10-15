# AI Agent Instructions for `syngen`

This file provides context and instructions for AI coding agents operating within this repository.

## Technology Stack

- **Language:** Go (Golang)
- **Cloud Storage:** AWS S3, Google Cloud Storage (GCS)
- **File Types:**
  - Parquet (Apache Arrow Format) (preferred for performance)
  - CSV (Comma Separated Values) (ideally only for seed data)

## AI Agent Rules/Guidelines

### 1. Do Not Commit Code

- **Never commit code automatically.** Your job is to write, modify, and test code. Leave the actual `git add`, `git commit`, and `git push` operations entirely to the user.

### 2. Verification Protocol

- **Always compile and lint.** After making *any* code changes, you must always run `go build` and execute the project linter (`golangci-lint run`) to verify your changes are syntactically correct and comply with project standards before returning control to the user.
- **Ensure schema validation passes.** After making any changes to the `schemas` directory, you must always run `go run cmd/schema-validator/main.go` to verify that the schema is valid.
- **Test when applicable.** Only run `go test` if there are test files present. Always run mock "tests" on any new code to verify functionality.

### 3. Design Philosophy (KISS & Performance)

- **Keep It Simple, Stupid (KISS).** Favor simple, readable, and straightforward implementations over overly clever or complex abstractions.
- **Performance is critical.** While keeping it simple, always stress performance. This engine processes millions of rows, so you must always consider CPU usage, memory overhead, and efficient data structures (e.g., zero-allocation techniques where appropriate).

### 4. Code Style & Commenting

- **Avoid unnecessary blank lines.** Do not add extra newlines in Go files just for visual padding. Keep the code compact.
- **Keep duplication to an absolute minimum.** Refactor duplicated code into helper functions or constants where appropriate.
- **Sparse, meaningful comments only.** Only add comments to explain *why* something complex was done, or to explicitly call out when a block of code intentionally goes against the usual way of doing things. Do not write trivial comments explaining *what* the code does.
- **Handle all errors gracefully.** Do not ignore errors with underscores assignment.
- **Environment variables should be used to control runtime behavior.** Do not add CLI arguments.

### 5. Cleanup

- **Always clean up after yourself.** This includes removing any temporary files you create and ensuring that the project is left in a clean state. Delete these files before returning control to the user.

### 6. Schema Validator

- **Ensure schema validator stays up to date.** `internal/validator/validator.go` validates all the schema files in the `schemas/` directory. Make sure to keep it up to date if any code changes alters the schema file structure or what constitutes valid key/values.

### 7. Testing

- **Ensure tests stay up to date.** `internal/generator/generator_test.go` and `internal/validator/validator_test.go` test the generator and schema validator. Make sure to keep these files along with any other test files up to date as code changes occur.

### 8. Documentation

- **The README is the source of truth.** Always refer to `README.md` as the primary source of project documentation.
- **Keep it updated.** Whenever you make changes that alter the system's architecture, setup, or usage, you must proactively update the `README.md` to reflect those changes.
