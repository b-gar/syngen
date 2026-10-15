# Syngen

**Syngen** is a high-performance **Go-based** synthetic data generator that creates CSV or Parquet files based on JSON schema definitions for data. It integrates natively with Kubernetes and S3.

## Features

- **High Performance**: Highly optimized single-threaded execution to maximize throughput while minimizing memory footprint, perfectly suited for Kubernetes distribution.
- **Ultra-Low Memory Profile**: S3 files are streamed via an 8MB sliding memory block cache rather than loaded into memory entirely, keeping heap allocations completely flat even for gigantic tables.
- **JSON Schema Driven**: JSON schema files with comprehensive key/value support for a wide variety of data generation capabilities.
- **Rich Data Types**: Support for string, integer, bigint, decimal, boolean, date, datetime, json_array, and json_object.
- **Realistic Data**: Realistic built-in synthetic data types for common data like names, addresses, etc. (via `gofakeit`).
- **Parquet Native**: Deep integration with Apache Arrow for blazing fast columnar Parquet generation and memory-efficient columnar reads.
- **Referential Integrity**: Directly read tables as "seeds" for other tables, preserving referential integrity.

## Getting Started

### Prerequisites

- Go 1.21+ (Go will automatically download the required version defined in `go.mod`)
- Docker (optional, for running the pre-packaged container instead)

### Setup and Build

1. **Clone the repository**:

   ```bash
   git clone git@github.com:b-gar/syngen.git
   cd syngen
   ```

2. **Install pre-commit and hooks**:

   ```bash
   pip install pre-commit && pre-commit install
   ```

3. **Build binary**:

   ```bash
   go build -o syngen ./cmd/syngen
   ```

4. **Add Go binaries to path** (optional):

   ```bash
   echo 'export PATH=$PATH:$(go env GOPATH)/bin' >> ~/.bashrc && source ~/.bashrc
   ```

## Development Commands

### Schema Validation

Ensures all JSON schema files strictly adhere to business rules and prevent runtime engine failures. The validator performs deep semantic checking of all schema files. It ensures that data types are valid, generator properties are correctly provided, cross-references (`@source.column`) map to valid explicitly defined sources, and catches orphaned sources.

```bash
go run cmd/schema-validator/main.go [optional/path/to/schemas/dir]
```

### Run Linter

Ensures the code matches linting standards.

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
golangci-lint run
```

### Run All Tests

```bash
go test ./...
```

#### Run Benchmark Tests

```bash
go test -bench . ./...
```

### Docker Deploy

A minimal distroless image is built via the CI pipeline.

```bash
docker build -t syngen:latest .
```

## Usage

The Go engine strictly uses **Environment Variables** for all execution configurations.

### Environment Variables

- `TABLE_NAME` (Required): The name of the table/schema to generate (e.g. `table`, `test/table2`). The engine automatically searches the `schemas/` directory for a matching file. If multiple schemas share the same basename in different subdirectories, you **must** use a path-qualified identifier (e.g. `test/table2`) to prevent ambiguity.
- `OUTPUT_PREFIX` (Required): The output directory or S3 bucket prefix where the generated files will be saved. (e.g. `s3://my-bucket/syngen/` or `output/`)
- `FORMAT` (Required): Output format (`parquet` or `csv`).
- `N_ROWS` (Required): The total number of rows to generate.
- `PARTITION` (Optional): The partition offset for distributed generation. Defaults to 0.
- `SEED` (Optional): Random seed for deterministic generation. If omitted, generation is non-deterministic.

### File Naming Convention

The engine automatically constructs the final output file path by appending the table name and partition directly to the `OUTPUT_PREFIX` string: `{OUTPUT_PREFIX}{TABLE_NAME}_part_{PARTITION}.{FORMAT}`. Because it does not insert any automatic slashes, you can easily pass custom or date-stamped file prefixes (e.g., `OUTPUT_PREFIX=s3://bucket/2026_09_15Z_` becomes `s3://bucket/2026_09_15Z_table_part_0.parquet`, and `OUTPUT_PREFIX=output/` becomes `output/table_part_0.parquet`).

### Running Locally

To generate 1,000 rows for the `my_table` schema locally:

```bash
TABLE_NAME="my_table" \
OUTPUT_PREFIX="output/" \
FORMAT="csv" \
N_ROWS=1000 \
./syngen
```

### Running Distributed

Syngen is intentionally designed to be run as a single-threaded process per pod to maximize Kubernetes cluster efficiency. To scale generation to hundreds of millions of rows, distribute the workload across multiple pods using the `PARTITION` environment variable.

If you want to generate 50 Million rows using 10 Pods, configure each pod to generate 5 Million rows and assign them an incrementing `PARTITION` number.

- Pod 0: `N_ROWS=5000000 PARTITION=0 OUTPUT_PREFIX=s3://bucket/<datetime>/`
- Pod 1: `N_ROWS=5000000 PARTITION=1 OUTPUT_PREFIX=s3://bucket/<datetime>/`

The `PARTITION` variable natively handles the Random Seed and Parent Source offsets so that Pod 1 generates exactly what would have been rows 5,000,000 to 10,000,000 in a single massive run. By using isolated `OUTPUT_PREFIX` directories, each pod acts entirely independently without file contention.

### Table Dependencies

To generate a table that depends on another table, first generate the parent table. The schema for the child table automatically reads the parent dimension table directly from the `OUTPUT_PREFIX`.

#### Generate Parent Table

```bash
TABLE_NAME="<parent_table>" \
OUTPUT_PREFIX="output/" \
FORMAT="parquet" \
N_ROWS=1000 \
./syngen
```

#### Generate Child Table

```bash
TABLE_NAME="<child_table>" \
OUTPUT_PREFIX="output/" \
FORMAT="parquet" \
N_ROWS=1000 \
./syngen
```

---

## JSON Schema Specification

This section outlines the JSON structure required for defining synthetic data generation schemas in the `schemas/` directory.

### Top-Level Structure

Every JSON schema file represents a single output table (e.g. a CSV or Parquet file). It must contain `columns`, and optionally `sources`.

```json
{
  "sources": {},
  "columns": [
    // Column definitions go here
  ]
}
```

### `sources` (Optional)

Used for defining external seed data (like Parquet tables or reference CSVs) that can be randomly or sequentially sampled.

- **`table`**: Name of a dynamically generated schema table. The engine resolves this using `OUTPUT_PREFIX`. This can be path-qualified if the parent table used a path-qualified identifier (e.g., `"test/table1"` resolves to `output/test/table1_part_X.parquet`).
- **`seed`**: Name of a static seed file. The engine resolves this from the local `seeds/` directory. You can provide an exact filename (e.g., `"states.csv"` resolves to `seeds/states.csv`). If you provide a name without an extension (e.g., `"states"`), the engine will automatically append the current run's global `FORMAT` extension (e.g., `seeds/states.parquet`).
- **`selection`**: How rows are chosen from the source file (defaults to `"next"` if omitted).
  - `"next"`: Reads rows sequentially and fails if it runs out of rows before reaching `N_ROWS`.
  - `"wrap"`: Reads rows sequentially, but automatically restarts from the top of the file if it runs out of rows before reaching `N_ROWS`.
  - `"random"`: Picks a random row from the file for every generated row. For Parquet sources, only the referenced lookup columns are loaded into compact columnar memory buffers for fast $O(1)$ random access; for CSV sources, random access seeks across byte offsets.

---

### Columns

The `columns` array defines the schema of the generated output. Each column requires a `name`, a `type`, and optionally a `generator` configuration block.

```json
{
  "name": "first_name",
  "type": "string",
  "null_percentage": 5,
  "generator": {
    "method": "first_name"
  }
}
```

#### Column Properties

- **`name`** (String, Required): The name of the column in the output file.
- **`type`** (String, Required): The data type of the column. Valid types: `"string"`, `"integer"`, `"bigint"`, `"decimal"`, `"boolean"`, `"date"`, `"datetime"`, `"json_array"`, `"json_object"`.
  - `"integer"`: 32-bit signed integer (Parquet INT32, range $-2^{31}$ to $2^{31}-1$).
  - `"bigint"`: 64-bit signed integer (Parquet INT64, range $-2^{63}$ to $2^{63}-1$). Use this when partition bounds, row counts, or IDs exceed 32-bit signed limits.
- **`precision`** (Integer, Optional): Used for `decimal` types to specify total number of digits (`1` to `38`). Must be paired with `scale` when specified (maps to Parquet `DECIMAL128(precision, scale)`; if both are omitted, Parquet defaults to `FLOAT64`).
- **`scale`** (Integer, Optional): Used for `decimal` types to specify the number of digits to the right of the decimal point (`0 <= scale <= precision`). Must be paired with `precision` when specified.
- **`null_percentage`** (Integer, Optional): A value between `0` and `100` dictating the probability that the engine will return an empty/null value for this column.
- **`null_value`** (Any, Optional): A specific value (e.g., `"~"`, `0`) to return if the `null_percentage` probability hits. If omitted, it returns standard `null`.
- **`generator`** (Object, Optional): The block defining the exact logic used to generate values for this column. If omitted, the engine falls back to default randomized generation based on the `type`.

---

### Generator Configuration (`generator` block)

The `generator` block contains **all** generation logic, bounds, and string modifiers for a column.

#### Core Properties

- **`method`** (String, Required): Explicitly declares the generation method.
  - Base Methods: `"constant"`, `"lookup"`, `"row_number"`, `"choices"`, `"range"`, `"regex"`.
  - Mock Methods: `"first_name"`, `"last_name"`, `"ssn"`, `"city_name"`, `"state_code"`, `"company_name"`, `"zip5_code"`, `"phone_number"`, `"street_address"`, `"email_address"`.
- **`transform`** (String, Optional): A post-processing modifier applied to string outputs.
  - Valid values: `"upper"`, `"lower"`, `"sha256"`.

#### Generator Method Specific Properties

##### 1. Mock Data Generators (e.g., `"method": "first_name"`, `"method": "ssn"`)

These types use the included realistic random generator library to produce realistic formatted data. No extra properties are strictly required, though `"max_length"` is highly recommended if inserting into fixed-length columns.

- **`max_length`** (Integer, Optional): Enforces a maximum character length on the generated string. The engine performs a high-speed retry loop to naturally find a mock value that fits the limit. If no value is found within the retry limit, it will truncate the value at the max_length.

##### 2. Row Number (`"method": "row_number"`)

Generates a simple, 1-indexed, auto-incrementing integer for each row. Useful for generating unique IDs. No extra properties are required.

##### 3. Constant (`"method": "constant"`)

Returns the exact value provided.

- **`value`** (Any, Required): The literal value to return in every row (e.g. `"Active"`, `123`). Alternatively, you can provide a reference (e.g. `"@test.col1"`). If a reference is provided here, the engine will pick a random value from that reference table, and use it for all rows. For `date` or `datetime` columns, relative time offsets (e.g., `"0d"`, `"-1y"`) are fully supported and dynamically resolved at execution time.

##### 4. Lookup (`"method": "lookup"`)

Looks up a dynamic value from the seed or parent table that matches the source name.

- **`value`** (String, Required): The lookup reference formatted as `@source_name.column_name` (e.g. `"@states.state_code"`). For each row generated, the engine selects a row from the source table according to the `selection` configuration defined in the schema's `sources` block (`next`, `wrap`, or `random`). *(Note: If multiple columns perform a lookup against the same source table, they will reliably pull from the exact same parent row to maintain relational integrity)*.

##### 5. Range (`"method": "range"`)

Generates a random value between a minimum and maximum bound. Applicable for `string` columns (using either date/datetime bounds or numeric bounds), and for `integer`, `bigint`, `decimal`, `date`, or `datetime` columns.

- **`min`** (Number/String, Required): The lower bound. (e.g., `1`, `"10000000000"`, `"2020-01-01"`, `"-5y"`, `"@start_date +1w"`).
- **`max`** (Number/String, Required): The upper bound. (e.g., `100`, `"99999999999"`, `"0d"`, `"+1m"`, `"+10y"`).
- *Note: Date/datetime bounds support standard formats `YYYY-MM-DD`, `YYYY-MM-DD HH:MM:SS`, or `RFC3339`, relative offsets using `d` (days), `w` (weeks), `m` (months), `y` (years) (e.g. `0d` for today, `-1w` for 1 week ago), and dynamic intra-row column references `@column_name` with optional offsets (e.g. `@col1 +1Y`). Referenced columns must be defined earlier in the same schema and provide date-compatible values. If the column `type` is `"string"`, numeric bounds emit a formatted numeric string and date bounds emit a date string formatted to match the bounds provided.*

##### 6. Choices (`"method": "choices"`)

Picks a random value from a predefined list.

- **`choices`** (Array, Required): The list of literal values to pick from (e.g., `["Y", "N"]`).
- **`weights`** (Array of Integers, Optional): Percentage probability for each choice. Must be exactly the same length as the `choices` array, and the values **must sum to exactly 100**. (e.g., `[95, 5]`).

##### 7. Regex (`"method": "regex"`)

Generates random characters matching a provided RE2 regular expression string. Use this for specifying string lengths.

- **`value`** (String, Required): The regular expression pattern. (e.g., `"[0-9]{3}-[A-Z]{3}"` -> `"123-ABC"`).
