package generator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"time"

	"github.com/b-gar/syngen/internal/schema"
	"github.com/brianvoe/gofakeit/v7"
)

var (
	offsetRegex         = regexp.MustCompile(`^([+-]?)(\d+)([dmywDMYW])$`)
	dynamicDateRefRegex = regexp.MustCompile(`^@([A-Za-z0-9_]+)(?:\s*([+-])\s*(\d+)([dmywDMYW]))?$`)
)

type ColumnGenerator func() (any, error)

type Generator struct {
	tbl            *schema.Table
	faker          *gofakeit.Faker
	sm             *SourceManager
	streamers      map[string]RowStreamer
	sourceRowCache map[string]map[string]any
	rowIndex       int64
	rowBuf         []any
	colGenerators  []ColumnGenerator
	colIndices     map[string]int
}

func NewGenerator(tbl *schema.Table, rowCount int, partition int, seed int64, sm *SourceManager) (gen *Generator, retErr error) {
	if err := ParseSchemaAST(tbl); err != nil {
		return nil, err
	}
	start, err := checkPartitionBounds(tbl, partition, rowCount)
	if err != nil {
		return nil, err
	}
	streamers, err := initSequentialStreamers(sm, tbl.Sources, start)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			retErr = errors.Join(retErr, closeStreamers(streamers))
		}
	}()
	cache := initSourceRowCache(sm)
	colIndices := make(map[string]int, len(tbl.Columns))
	for i, col := range tbl.Columns {
		colIndices[strings.ToLower(col.Name)] = i
	}
	faker := gofakeit.New(uint64(seed + int64(partition)))
	faker.Locked = false
	g := &Generator{
		tbl:            tbl,
		faker:          faker,
		sm:             sm,
		streamers:      streamers,
		sourceRowCache: cache,
		rowIndex:       start,
		rowBuf:         make([]any, len(tbl.Columns)),
		colGenerators:  make([]ColumnGenerator, len(tbl.Columns)),
		colIndices:     colIndices,
	}
	for i := range tbl.Columns {
		colGen := g.buildColumnGenerator(&tbl.Columns[i])
		if colGen == nil {
			return nil, fmt.Errorf("failed to build column generator for column '%s' (method '%s', type '%s')", tbl.Columns[i].Name, tbl.Columns[i].Generator.Method, tbl.Columns[i].Type)
		}
		g.colGenerators[i] = colGen
	}
	success = true
	return g, nil
}

func checkPartitionBounds(tbl *schema.Table, partition, rowCount int) (int64, error) {
	p, rc := int64(partition), int64(rowCount)
	if p > 0 && rc > math.MaxInt64/p {
		return 0, fmt.Errorf("partition start bound overflows int64: partition=%d, rowCount=%d", partition, rowCount)
	}
	start := p * rc
	if math.MaxInt64-start < rc {
		return 0, fmt.Errorf("partition end bound overflows int64: start=%d, rowCount=%d", start, rowCount)
	}
	end := start + rc
	if end > math.MaxInt32 {
		for i := range tbl.Columns {
			col := &tbl.Columns[i]
			if col.Generator.Method == "row_number" && col.Type == "integer" {
				return 0, fmt.Errorf("column '%s' uses 'row_number' with type 'integer', but requested partition bounds exceed max int32. Use 'bigint' instead", col.Name)
			}
		}
	}
	return start, nil
}

func initSequentialStreamers(sm *SourceManager, sources map[string]schema.SourceConfig, start int64) (map[string]RowStreamer, error) {
	streamers := make(map[string]RowStreamer)
	if sm == nil {
		return streamers, nil
	}
	for name, cfg := range sources {
		if cfg.Selection == "random" {
			continue
		}
		streamer, err := sm.NewStreamer(name, start, false)
		if err != nil {
			err = errors.Join(err, closeStreamers(streamers))
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("not enough rows in seed file for source '%s' to satisfy requested row count sequentially: %w", name, err)
			}
			return nil, fmt.Errorf("error initializing source '%s': %w", name, err)
		}
		streamers[name] = streamer
	}
	return streamers, nil
}

func initSourceRowCache(sm *SourceManager) map[string]map[string]any {
	cache := make(map[string]map[string]any)
	if sm != nil {
		for name := range sm.Configs {
			cache[name] = make(map[string]any)
		}
	}
	return cache
}

func (g *Generator) NextRow() ([]any, error) {
	if g.sm != nil {
		for _, m := range g.sourceRowCache {
			clear(m)
		}
		for name, streamer := range g.streamers {
			if err := advanceStreamer(name, streamer, g.sourceRowCache[name], g.sm.Configs[name]); err != nil {
				return nil, err
			}
		}
	}
	for i, colGen := range g.colGenerators {
		val, err := colGen()
		if err != nil {
			return nil, err
		}
		g.rowBuf[i] = val
	}
	g.rowIndex++
	return g.rowBuf, nil
}

func advanceStreamer(name string, streamer RowStreamer, cache map[string]any, cfg schema.SourceConfig) error {
	err := streamer.NextRow(cache)
	if err == nil {
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("error reading next row from source '%s': %w", name, err)
	}
	if cfg.Selection != "wrap" {
		return fmt.Errorf("source '%s' exhausted and selection is not wrap", name)
	}
	if err := streamer.Reset(); err != nil {
		return fmt.Errorf("error resetting source '%s': %w", name, err)
	}
	if err := streamer.NextRow(cache); err != nil {
		return fmt.Errorf("error reading next row from source '%s' after reset: %w", name, err)
	}
	return nil
}

func (g *Generator) Close() error {
	return closeStreamers(g.streamers)
}

func closeStreamers(streamers map[string]RowStreamer) error {
	var errs []error
	for _, s := range streamers {
		if err := s.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func ParseSchemaAST(tbl *schema.Table) error {
	var errs []error
	colIndices := make(map[string]int, len(tbl.Columns))
	for i, col := range tbl.Columns {
		colIndices[strings.ToLower(col.Name)] = i
	}
	for i := range tbl.Columns {
		if err := ParseColumnAST(&tbl.Columns[i]); err != nil {
			errs = append(errs, fmt.Errorf("error parsing column '%s': %w", tbl.Columns[i].Name, err))
		}
		gen := &tbl.Columns[i].Generator
		if gen.ParsedMinDynamicRef != "" {
			idx, ok := colIndices[strings.ToLower(gen.ParsedMinDynamicRef)]
			if !ok {
				errs = append(errs, fmt.Errorf("column '%s' dynamic date bound references undefined column '%s'", tbl.Columns[i].Name, gen.ParsedMinDynamicRef))
			} else if idx >= i {
				errs = append(errs, fmt.Errorf("column '%s' dynamic date bound references column '%s' which does not appear earlier in the table", tbl.Columns[i].Name, gen.ParsedMinDynamicRef))
			} else if refType := tbl.Columns[idx].Type; refType != "date" && refType != "datetime" && refType != "string" {
				errs = append(errs, fmt.Errorf("column '%s' dynamic date bound references non-date column '%s' of type '%s'", tbl.Columns[i].Name, gen.ParsedMinDynamicRef, refType))
			} else {
				gen.ParsedMinDynamicIdx = idx
			}
		}
		if gen.ParsedMaxDynamicRef != "" {
			idx, ok := colIndices[strings.ToLower(gen.ParsedMaxDynamicRef)]
			if !ok {
				errs = append(errs, fmt.Errorf("column '%s' dynamic date bound references undefined column '%s'", tbl.Columns[i].Name, gen.ParsedMaxDynamicRef))
			} else if idx >= i {
				errs = append(errs, fmt.Errorf("column '%s' dynamic date bound references column '%s' which does not appear earlier in the table", tbl.Columns[i].Name, gen.ParsedMaxDynamicRef))
			} else if refType := tbl.Columns[idx].Type; refType != "date" && refType != "datetime" && refType != "string" {
				errs = append(errs, fmt.Errorf("column '%s' dynamic date bound references non-date column '%s' of type '%s'", tbl.Columns[i].Name, gen.ParsedMaxDynamicRef, refType))
			} else {
				gen.ParsedMaxDynamicIdx = idx
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func roundDecimalAST(val any, gen *schema.GeneratorConfig) any {
	if gen.ParsedScale == nil {
		return val
	}
	f, ok := val.(float64)
	if !ok {
		return val
	}
	if gen.ParsedPrecision != nil && *gen.ParsedPrecision >= *gen.ParsedScale {
		f = math.Mod(f, math.Pow(10, float64(*gen.ParsedPrecision-*gen.ParsedScale)))
	}
	return math.Round(f*gen.ParsedScaleShift) / gen.ParsedScaleShift
}

func ParseColumnAST(col *schema.Column) error {
	gen := &col.Generator
	dataType := col.Type
	gen.ParsedPrecision = col.Precision
	gen.ParsedScale = col.Scale
	if gen.ParsedScale != nil {
		gen.ParsedScaleShift = math.Pow(10, float64(*gen.ParsedScale))
	}
	if col.NullValue != nil {
		val, err := ensureType(col.NullValue, dataType, gen.ParsedScale != nil)
		if err != nil {
			return err
		}
		if dataType == "decimal" {
			val = roundDecimalAST(val, gen)
		}
		col.ParsedNullValue = val
	}
	switch gen.Method {
	case "constant", "lookup", "":
		return parseConstantOrLookupAST(gen, dataType)
	case "choices":
		return parseChoicesAST(gen, dataType)
	case "range":
		return parseRangeAST(gen, dataType)
	case "regex":
		return parseRegexAST(gen)
	}
	return nil
}

func parseConstantOrLookupAST(gen *schema.GeneratorConfig, dataType string) error {
	if gen.Value != nil {
		strVal, ok := gen.Value.(string)
		if ok && strings.HasPrefix(strVal, "@") {
			parts := strings.SplitN(strings.TrimPrefix(strVal, "@"), ".", 2)
			if len(parts) == 2 {
				gen.IsLookup = true
				gen.ParsedLookupSource = parts[0]
				gen.ParsedLookupColumn = parts[1]
			}
		}
		if !gen.IsLookup {
			val, err := ensureType(gen.Value, dataType, gen.ParsedScale != nil)
			if err != nil {
				return err
			}
			if dataType == "decimal" {
				val = roundDecimalAST(val, gen)
			}
			gen.ParsedValue = val
		}
	}
	return nil
}

func parseChoicesAST(gen *schema.GeneratorConfig, dataType string) error {
	if len(gen.Choices) > 0 {
		gen.ParsedChoices = make([]any, len(gen.Choices))
		for j, c := range gen.Choices {
			val, err := ensureType(c, dataType, gen.ParsedScale != nil)
			if err != nil {
				return err
			}
			if dataType == "decimal" {
				val = roundDecimalAST(val, gen)
			}
			gen.ParsedChoices[j] = val
		}
		if len(gen.Weights) == len(gen.ParsedChoices) {
			gen.ParsedCumulativeWeights = make([]int, len(gen.Weights))
			cumulative := 0
			for i, w := range gen.Weights {
				cumulative += w
				gen.ParsedCumulativeWeights[i] = cumulative
			}
			gen.TotalWeight = cumulative
		}
	}
	return nil
}

func parseRegexAST(gen *schema.GeneratorConfig) error {
	if gen.Value == nil {
		return fmt.Errorf("regex method requires a value string")
	}
	strVal, ok := gen.Value.(string)
	if !ok || strVal == "" {
		return fmt.Errorf("regex method requires a non-empty string value")
	}
	if gen.FastRegexFunc != nil && gen.ParsedValue == strVal {
		return nil
	}
	if _, err := syntax.Parse(strVal, syntax.Perl); err != nil {
		return fmt.Errorf("invalid regex pattern '%s': %w", strVal, err)
	}
	gen.ParsedValue = strVal
	gen.FastRegexFunc = CompileRegex(strVal)
	return nil
}

func parseRangeAST(gen *schema.GeneratorConfig, dataType string) error {
	gen.IsRangeCached = true
	switch dataType {
	case "date", "datetime":
		return parseDateRangeAST(gen)
	case "string":
		return parseStringRangeAST(gen)
	case "integer":
		return parseIntegerRangeAST(gen)
	case "bigint":
		return parseBigintRangeAST(gen)
	case "decimal":
		return parseDecimalRangeAST(gen)
	default:
		return fmt.Errorf("type '%s' does not support 'range' method", dataType)
	}
}

func parseDateRangeAST(gen *schema.GeneratorConfig) error {
	minD, err := parseDateOrDynamicBound(fmt.Sprintf("%v", gen.Min), func(ref, sign string, num int, unit string) {
		gen.ParsedMinDynamicRef = ref
		gen.ParsedMinDynamicSign = sign
		gen.ParsedMinDynamicNum = num
		gen.ParsedMinDynamicUnit = unit
	})
	if err != nil {
		return fmt.Errorf("min date bound: %w", err)
	}
	gen.ParsedMinDate = minD
	maxD, err := parseDateOrDynamicBound(fmt.Sprintf("%v", gen.Max), func(ref, sign string, num int, unit string) {
		gen.ParsedMaxDynamicRef = ref
		gen.ParsedMaxDynamicSign = sign
		gen.ParsedMaxDynamicNum = num
		gen.ParsedMaxDynamicUnit = unit
	})
	if err != nil {
		return fmt.Errorf("max date bound: %w", err)
	}
	gen.ParsedMaxDate = maxD
	if gen.ParsedMinDynamicRef == "" && gen.ParsedMaxDynamicRef == "" && gen.ParsedMinDate.After(gen.ParsedMaxDate) {
		gen.ParsedMinDate, gen.ParsedMaxDate = gen.ParsedMaxDate, gen.ParsedMinDate
	}
	return nil
}

func parseStringRangeAST(gen *schema.GeneratorConfig) error {
	minStr := fmt.Sprintf("%v", gen.Min)
	maxStr := fmt.Sprintf("%v", gen.Max)
	if tryParseStringDateRange(gen, minStr, maxStr) {
		return nil
	}
	if tryParseStringNumericRange(gen, minStr, maxStr) {
		return nil
	}
	return fmt.Errorf("string range requires date/datetime bounds or numeric bounds, got min='%s' max='%s'", minStr, maxStr)
}

func tryParseStringDateRange(gen *schema.GeneratorConfig, minStr, maxStr string) bool {
	isMinDynamic := strings.HasPrefix(strings.TrimSpace(minStr), "@")
	isMaxDynamic := strings.HasPrefix(strings.TrimSpace(maxStr), "@")
	if isMinDynamic || isMaxDynamic {
		if err := parseDateRangeAST(gen); err == nil {
			gen.IsDateStringRange = true
			if _, maxFmt, ok := tryParseDateBound(maxStr); ok && maxFmt != "" {
				gen.ParsedDateFormat = maxFmt
			} else if _, minFmt, ok := tryParseDateBound(minStr); ok && minFmt != "" {
				gen.ParsedDateFormat = minFmt
			} else {
				gen.ParsedDateFormat = time.DateOnly
			}
			return true
		}
	}
	minD, minFmt, ok1 := tryParseDateBound(minStr)
	maxD, maxFmt, ok2 := tryParseDateBound(maxStr)
	if ok1 && ok2 {
		gen.IsDateStringRange = true
		gen.ParsedMinDate = minD
		gen.ParsedMaxDate = maxD
		if len(maxFmt) > len(minFmt) {
			gen.ParsedDateFormat = maxFmt
		} else {
			gen.ParsedDateFormat = minFmt
		}
		if gen.ParsedMinDate.After(gen.ParsedMaxDate) {
			gen.ParsedMinDate, gen.ParsedMaxDate = gen.ParsedMaxDate, gen.ParsedMinDate
		}
		return true
	}
	return false
}

func tryParseStringNumericRange(gen *schema.GeneratorConfig, minStr, maxStr string) bool {
	if minI, err1 := strconv.ParseInt(minStr, 10, 64); err1 == nil {
		if maxI, err2 := strconv.ParseInt(maxStr, 10, 64); err2 == nil {
			gen.ParsedMinBigint = minI
			gen.ParsedMaxBigint = maxI
			if gen.ParsedMinBigint > gen.ParsedMaxBigint {
				gen.ParsedMinBigint, gen.ParsedMaxBigint = gen.ParsedMaxBigint, gen.ParsedMinBigint
			}
			gen.IsIntStringRange = true
			return true
		}
	}
	if minF, err1 := strconv.ParseFloat(minStr, 64); err1 == nil && !math.IsNaN(minF) && !math.IsInf(minF, 0) {
		if maxF, err2 := strconv.ParseFloat(maxStr, 64); err2 == nil && !math.IsNaN(maxF) && !math.IsInf(maxF, 0) {
			gen.ParsedMinFloat = minF
			gen.ParsedMaxFloat = maxF
			if gen.ParsedMinFloat > gen.ParsedMaxFloat {
				gen.ParsedMinFloat, gen.ParsedMaxFloat = gen.ParsedMaxFloat, gen.ParsedMinFloat
			}
			gen.IsFloatStringRange = true
			return true
		}
	}
	return false
}

func parseIntegerRangeAST(gen *schema.GeneratorConfig) error {
	minVal, err := parseBoundInt64(gen.Min)
	if err != nil {
		return err
	}
	maxVal, err := parseBoundInt64(gen.Max)
	if err != nil {
		return err
	}
	if minVal > math.MaxInt32 || minVal < math.MinInt32 || maxVal > math.MaxInt32 || maxVal < math.MinInt32 {
		return fmt.Errorf("range bounds exceed int32 limits. use 'bigint' type")
	}
	gen.ParsedMinInt = int(minVal)
	gen.ParsedMaxInt = int(maxVal)
	if gen.ParsedMinInt > gen.ParsedMaxInt {
		gen.ParsedMinInt, gen.ParsedMaxInt = gen.ParsedMaxInt, gen.ParsedMinInt
	}
	return nil
}

func parseBigintRangeAST(gen *schema.GeneratorConfig) error {
	var err error
	if gen.ParsedMinBigint, err = parseBoundInt64(gen.Min); err != nil {
		return err
	}
	if gen.ParsedMaxBigint, err = parseBoundInt64(gen.Max); err != nil {
		return err
	}
	if gen.ParsedMinBigint > gen.ParsedMaxBigint {
		gen.ParsedMinBigint, gen.ParsedMaxBigint = gen.ParsedMaxBigint, gen.ParsedMinBigint
	}
	return nil
}

func parseDecimalRangeAST(gen *schema.GeneratorConfig) error {
	var err error
	if gen.ParsedMinFloat, err = parseBoundFloat64(gen.Min); err != nil {
		return err
	}
	if gen.ParsedMaxFloat, err = parseBoundFloat64(gen.Max); err != nil {
		return err
	}
	if gen.ParsedMinFloat > gen.ParsedMaxFloat {
		gen.ParsedMinFloat, gen.ParsedMaxFloat = gen.ParsedMaxFloat, gen.ParsedMinFloat
	}
	return nil
}

func FindRequiredColumns(tbl *schema.Table) map[string][]string {
	reqs := make(map[string]map[string]bool)
	for i := range tbl.Columns {
		gen := &tbl.Columns[i].Generator
		if gen.IsLookup {
			src := gen.ParsedLookupSource
			col := gen.ParsedLookupColumn
			if reqs[src] == nil {
				reqs[src] = make(map[string]bool)
			}
			reqs[src][col] = true
		}
	}
	res := make(map[string][]string)
	for src, cols := range reqs {
		var list []string
		for col := range cols {
			list = append(list, col)
		}
		res[src] = list
	}
	return res
}

func (g *Generator) buildColumnGenerator(col *schema.Column) ColumnGenerator {
	gen := &col.Generator
	baseGen := g.buildBaseColumnGen(col)
	if baseGen == nil {
		return nil
	}
	if col.Type == "decimal" && gen.ParsedScale != nil && (gen.Method == "range" || gen.Method == "" || gen.IsLookup) {
		baseGen = wrapDecimalScale(baseGen, gen)
	}
	if gen.Transform != "" {
		baseGen = wrapTransform(baseGen, gen.Transform)
	}
	if col.NullPercentage > 0 {
		baseGen = wrapNullPercentage(baseGen, col.NullPercentage, col.NullValue != nil, col.ParsedNullValue, g.faker)
	}
	return baseGen
}

func (g *Generator) buildBaseColumnGen(col *schema.Column) ColumnGenerator {
	gen := &col.Generator
	if gen.Method == "constant" || gen.Method == "lookup" || gen.Method == "" {
		if baseGen := g.buildConstantOrLookupGen(col); baseGen != nil {
			return baseGen
		}
	}
	switch gen.Method {
	case "row_number":
		return func() (any, error) { return g.rowIndex + 1, nil }
	case "first_name", "last_name", "city_name", "state_code", "company_name", "zip5_code", "phone_number", "street_address", "email_address", "ssn":
		return g.buildMockGen(gen)
	case "choices":
		return g.buildChoicesGen(gen)
	case "regex":
		return g.buildRegexGen(gen)
	case "range":
		return g.buildRangeGen(gen, col.Type)
	default:
		return g.buildFallbackGen(col.Type)
	}
}

func wrapDecimalScale(inner ColumnGenerator, gen *schema.GeneratorConfig) ColumnGenerator {
	precision := gen.ParsedPrecision
	scale := gen.ParsedScale
	scaleShift := gen.ParsedScaleShift
	var maxInt float64
	if precision != nil && *precision >= *scale {
		maxInt = math.Pow(10, float64(*precision-*scale))
	}
	return func() (any, error) {
		val, err := inner()
		if err != nil || val == nil {
			return val, err
		}
		if f, ok := val.(float64); ok {
			if maxInt > 0 {
				f = math.Mod(f, maxInt)
			}
			return math.Round(f*scaleShift) / scaleShift, nil
		}
		return val, nil
	}
}

func wrapTransform(inner ColumnGenerator, transform string) ColumnGenerator {
	return func() (any, error) {
		val, err := inner()
		if err != nil || val == nil {
			return val, err
		}
		if strVal, ok := val.(string); ok {
			switch transform {
			case "upper":
				return strings.ToUpper(strVal), nil
			case "lower":
				return strings.ToLower(strVal), nil
			case "sha256":
				sum := sha256.Sum256([]byte(strVal))
				return hex.EncodeToString(sum[:]), nil
			}
		}
		return val, nil
	}
}

func wrapNullPercentage(inner ColumnGenerator, nullPct int, hasNullVal bool, nullVal any, faker *gofakeit.Faker) ColumnGenerator {
	return func() (any, error) {
		if faker.IntRange(1, 100) <= nullPct {
			if hasNullVal {
				return nullVal, nil
			}
			return nil, nil
		}
		return inner()
	}
}

func (g *Generator) buildConstantOrLookupGen(col *schema.Column) ColumnGenerator {
	gen := &col.Generator
	if !gen.IsLookup || g.sm == nil {
		if gen.Value != nil {
			val := gen.ParsedValue
			return func() (any, error) { return val, nil }
		}
		return nil
	}
	sourceName := gen.ParsedLookupSource
	colName := gen.ParsedLookupColumn
	rowCache := g.getOrCreateSourceRowCache(sourceName)
	cfg, hasCfg := g.sm.Configs[sourceName]
	isRandom := hasCfg && cfg.Selection == "random"
	var randomStreamer RowStreamer
	var randomTotal int64
	if isRandom {
		randomStreamer = g.sm.RandomStreamers[sourceName]
		randomTotal = g.sm.RandomTotals[sourceName]
	}
	return func() (any, error) {
		if !gen.IsLookup {
			return gen.ParsedValue, nil
		}
		if len(rowCache) == 0 && isRandom {
			if err := g.fetchRandomSourceRow(sourceName, rowCache, randomStreamer, randomTotal); err != nil {
				return nil, err
			}
		}
		if len(rowCache) == 0 {
			return nil, fmt.Errorf("no row data available from source '%s' for column '%s'", sourceName, col.Name)
		}
		val, ok := rowCache[colName]
		if !ok {
			return nil, fmt.Errorf("column '%s' not found in source '%s' for column '%s'", colName, sourceName, col.Name)
		}
		parsedVal, err := extractLookupValue(col, val)
		if err != nil {
			return nil, err
		}
		if gen.Method == "constant" {
			gen.ParsedValue = parsedVal
			gen.IsLookup = false
		}
		return parsedVal, nil
	}
}

func (g *Generator) getOrCreateSourceRowCache(sourceName string) map[string]any {
	rowCache, ok := g.sourceRowCache[sourceName]
	if !ok {
		rowCache = make(map[string]any)
		g.sourceRowCache[sourceName] = rowCache
	}
	return rowCache
}

func (g *Generator) fetchRandomSourceRow(sourceName string, rowCache map[string]any, streamer RowStreamer, total int64) error {
	if streamer == nil || total <= 0 {
		return nil
	}
	if err := streamer.SeekRow(g.int64Range(0, total-1), false); err != nil {
		return fmt.Errorf("error seeking random row for source '%s': %w", sourceName, err)
	}
	if err := streamer.NextRow(rowCache); err != nil {
		return fmt.Errorf("error reading random row for source '%s': %w", sourceName, err)
	}
	return nil
}

func extractLookupValue(col *schema.Column, val any) (any, error) {
	gen := &col.Generator
	dataType := col.Type
	if dataType == "string" {
		return val, nil
	}
	parsedVal, err := ensureType(val, dataType, gen.ParsedScale != nil)
	if err != nil {
		return nil, fmt.Errorf("failed to convert lookup value %v from source '%s' column '%s' to %s for column '%s': %w", val, gen.ParsedLookupSource, gen.ParsedLookupColumn, dataType, col.Name, err)
	}
	return parsedVal, nil
}

func fixedMockLength(method string) int {
	switch method {
	case "state_code":
		return 2
	case "zip5_code":
		return 5
	case "phone_number":
		return 10
	case "ssn":
		return 11
	default:
		return 0
	}
}

func (g *Generator) buildMockGen(gen *schema.GeneratorConfig) ColumnGenerator {
	method := gen.Method
	if gen.MaxLength != nil && *gen.MaxLength > 0 {
		maxLen := *gen.MaxLength
		if fixedLen := fixedMockLength(method); fixedLen > 0 {
			if maxLen >= fixedLen {
				return func() (any, error) { return g.generateMockString(method), nil }
			}
			return func() (any, error) { return g.generateMockString(method)[:maxLen], nil }
		}
		return func() (any, error) {
			for range 10 {
				val := g.generateMockString(method)
				if len(val) <= maxLen {
					return val, nil
				}
			}
			val := g.generateMockString(method)
			if len(val) > maxLen {
				return val[:maxLen], nil
			}
			return val, nil
		}
	}
	return func() (any, error) { return g.generateMockString(method), nil }
}

func (g *Generator) buildChoicesGen(gen *schema.GeneratorConfig) ColumnGenerator {
	if len(gen.ParsedChoices) > 0 {
		if gen.TotalWeight > 0 {
			return func() (any, error) {
				r := g.faker.IntRange(1, gen.TotalWeight)
				for i, cum := range gen.ParsedCumulativeWeights {
					if r <= cum {
						return gen.ParsedChoices[i], nil
					}
				}
				return gen.ParsedChoices[g.faker.IntRange(0, len(gen.ParsedChoices)-1)], nil
			}
		}
		return func() (any, error) {
			return gen.ParsedChoices[g.faker.IntRange(0, len(gen.ParsedChoices)-1)], nil
		}
	}
	return nil
}

func (g *Generator) buildRegexGen(gen *schema.GeneratorConfig) ColumnGenerator {
	if gen.FastRegexFunc != nil {
		return func() (any, error) { return gen.FastRegexFunc(g.faker), nil }
	} else if strVal, ok := gen.ParsedValue.(string); ok {
		return func() (any, error) { return g.faker.Regex(strVal), nil }
	}
	return nil
}

func (g *Generator) buildRangeGen(gen *schema.GeneratorConfig, dataType string) ColumnGenerator {
	if !gen.IsRangeCached {
		return nil
	}
	if gen.IsDateStringRange {
		if gen.ParsedMinDynamicRef == "" && gen.ParsedMaxDynamicRef == "" {
			return func() (any, error) {
				return g.faker.DateRange(gen.ParsedMinDate, gen.ParsedMaxDate).Format(gen.ParsedDateFormat), nil
			}
		}
		return func() (any, error) {
			minD, maxD, err := g.resolveRangeDates(gen)
			if err != nil {
				return nil, err
			}
			return g.faker.DateRange(minD, maxD).Format(gen.ParsedDateFormat), nil
		}
	} else if gen.IsIntStringRange {
		if gen.ParsedMinBigint == gen.ParsedMaxBigint {
			val := strconv.FormatInt(gen.ParsedMinBigint, 10)
			return func() (any, error) { return val, nil }
		}
		return func() (any, error) {
			return strconv.FormatInt(g.int64Range(gen.ParsedMinBigint, gen.ParsedMaxBigint), 10), nil
		}
	} else if gen.IsFloatStringRange {
		return func() (any, error) {
			return strconv.FormatFloat(g.faker.Float64Range(gen.ParsedMinFloat, gen.ParsedMaxFloat), 'f', -1, 64), nil
		}
	} else {
		switch dataType {
		case "date", "datetime":
			if gen.ParsedMinDynamicRef == "" && gen.ParsedMaxDynamicRef == "" {
				return func() (any, error) {
					return g.faker.DateRange(gen.ParsedMinDate, gen.ParsedMaxDate).UnixMilli(), nil
				}
			}
			return func() (any, error) {
				minD, maxD, err := g.resolveRangeDates(gen)
				if err != nil {
					return nil, err
				}
				return g.faker.DateRange(minD, maxD).UnixMilli(), nil
			}
		case "integer":
			if gen.ParsedMinInt == gen.ParsedMaxInt {
				val := int32(gen.ParsedMinInt)
				return func() (any, error) { return val, nil }
			}
			return func() (any, error) { return int32(g.faker.IntRange(gen.ParsedMinInt, gen.ParsedMaxInt)), nil }
		case "bigint":
			if gen.ParsedMinBigint == gen.ParsedMaxBigint {
				val := gen.ParsedMinBigint
				return func() (any, error) { return val, nil }
			}
			return func() (any, error) { return g.int64Range(gen.ParsedMinBigint, gen.ParsedMaxBigint), nil }
		case "decimal":
			return func() (any, error) { return g.faker.Float64Range(gen.ParsedMinFloat, gen.ParsedMaxFloat), nil }
		}
	}
	return nil
}

func (g *Generator) resolveRangeDates(gen *schema.GeneratorConfig) (time.Time, time.Time, error) {
	minD := gen.ParsedMinDate
	if gen.ParsedMinDynamicRef != "" {
		base, ok := resolveRowDate(g.rowBuf[gen.ParsedMinDynamicIdx])
		if !ok {
			return time.Time{}, time.Time{}, fmt.Errorf("referenced column '%s' did not provide a valid date", gen.ParsedMinDynamicRef)
		}
		minD = applyDateOffset(base, gen.ParsedMinDynamicSign, gen.ParsedMinDynamicNum, gen.ParsedMinDynamicUnit)
	}
	maxD := gen.ParsedMaxDate
	if gen.ParsedMaxDynamicRef != "" {
		base, ok := resolveRowDate(g.rowBuf[gen.ParsedMaxDynamicIdx])
		if !ok {
			return time.Time{}, time.Time{}, fmt.Errorf("referenced column '%s' did not provide a valid date", gen.ParsedMaxDynamicRef)
		}
		maxD = applyDateOffset(base, gen.ParsedMaxDynamicSign, gen.ParsedMaxDynamicNum, gen.ParsedMaxDynamicUnit)
	}
	if minD.After(maxD) {
		minD, maxD = maxD, minD
	}
	return minD, maxD, nil
}

func (g *Generator) buildFallbackGen(dataType string) ColumnGenerator {
	switch dataType {
	case "json_array":
		return func() (any, error) {
			var buf [128]byte
			b := buf[:0]
			b = append(b, '[')
			b = strconv.AppendInt(b, int64(g.faker.IntRange(1, 100)), 10)
			b = append(b, []byte(`, "`)...)
			b = append(b, g.faker.Word()...)
			b = append(b, []byte(`", `)...)
			b = strconv.AppendBool(b, g.faker.Bool())
			b = append(b, ']')
			return string(b), nil
		}
	case "json_object":
		return func() (any, error) {
			var buf [128]byte
			b := buf[:0]
			b = append(b, []byte(`{"`)...)
			b = append(b, g.faker.Word()...)
			b = append(b, []byte(`": "`)...)
			b = append(b, g.faker.Word()...)
			b = append(b, []byte(`", "id": `)...)
			b = strconv.AppendInt(b, int64(g.faker.IntRange(1, 1000)), 10)
			b = append(b, '}')
			return string(b), nil
		}
	case "integer":
		return func() (any, error) { return int32(g.faker.Int32()), nil }
	case "bigint":
		return func() (any, error) { return int64(g.faker.Int64()), nil }
	case "decimal":
		return func() (any, error) { return float64(g.faker.Float64()), nil }
	case "boolean":
		return func() (any, error) { return g.faker.Bool(), nil }
	case "date", "datetime":
		return func() (any, error) { return g.faker.Date().UnixMilli(), nil }
	default:
		return func() (any, error) { return g.faker.LetterN(10), nil }
	}
}

func writeDigits(buf []byte, val int) {
	for i := len(buf) - 1; i >= 0; i-- {
		buf[i] = byte('0' + val%10)
		val /= 10
	}
}

func ensureType(val any, t string, isDecimal128 bool) (any, error) {
	if val == nil {
		return nil, nil
	}
	val = normalizeJSONNumber(val, t, isDecimal128)
	switch t {
	case "integer":
		return ensureInteger(val)
	case "bigint":
		return ensureBigint(val)
	case "decimal":
		return ensureDecimal(val, isDecimal128)
	case "boolean":
		return ensureBoolean(val)
	case "date", "datetime":
		return ensureDate(val, t)
	default:
		return val, nil
	}
}

func normalizeJSONNumber(val any, t string, isDecimal128 bool) any {
	n, ok := val.(json.Number)
	if !ok {
		return val
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	if t == "decimal" && isDecimal128 {
		return string(n)
	}
	if t == "integer" || t == "bigint" {
		return string(n)
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return string(n)
}

func ensureInteger(val any) (any, error) {
	checkInt32 := func(v int64) (any, error) {
		if v > math.MaxInt32 || v < math.MinInt32 {
			return nil, fmt.Errorf("value %d exceeds int32 bounds, use 'bigint' type", v)
		}
		return int32(v), nil
	}
	switch v := val.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v > math.MaxInt64 || v < math.MinInt64 {
			return nil, fmt.Errorf("value %v exceeds int32 bounds, use 'bigint' type", v)
		}
		return checkInt32(int64(v))
	case int:
		return checkInt32(int64(v))
	case int32:
		return v, nil
	case int64:
		return checkInt32(v)
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid integer: %w", v, err)
		}
		return checkInt32(i)
	default:
		return nil, fmt.Errorf("value %v of type %T cannot be converted to integer", val, val)
	}
}

func ensureBigint(val any) (any, error) {
	switch v := val.(type) {
	case float64:
		if v >= 9223372036854775808.0 || v < -9223372036854775808.0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("value %v exceeds int64 bounds", v)
		}
		return int64(v), nil
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid bigint: %w", v, err)
		}
		return i, nil
	default:
		return nil, fmt.Errorf("value %v of type %T cannot be converted to bigint", val, val)
	}
}

func ensureDecimal(val any, isDecimal128 bool) (any, error) {
	switch v := val.(type) {
	case int:
		if isDecimal128 {
			return int64(v), nil
		}
		return float64(v), nil
	case int32:
		if isDecimal128 {
			return int64(v), nil
		}
		return float64(v), nil
	case int64:
		if isDecimal128 {
			return v, nil
		}
		return float64(v), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("value %v is not a valid decimal", v)
		}
		return v, nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("value %q is not a valid decimal", v)
		}
		if isDecimal128 {
			if i, err := strconv.ParseInt(v, 10, 64); err == nil {
				return i, nil
			}
			return v, nil
		}
		return f, nil
	default:
		return nil, fmt.Errorf("value %v of type %T cannot be converted to decimal", val, val)
	}
}

func ensureBoolean(val any) (any, error) {
	switch v := val.(type) {
	case bool:
		return v, nil
	case string:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("value %q is not a valid boolean: %w", v, err)
		}
		return b, nil
	case int:
		return v != 0, nil
	case int32:
		return v != 0, nil
	case int64:
		return v != 0, nil
	default:
		return nil, fmt.Errorf("value %v of type %T cannot be converted to boolean", val, val)
	}
}

func ensureDate(val any, t string) (any, error) {
	switch v := val.(type) {
	case string:
		parsedTime, _, ok := tryParseDateBound(v)
		if !ok {
			return nil, fmt.Errorf("value %q is not a valid %s", v, t)
		}
		return parsedTime.UnixMilli(), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	default:
		return nil, fmt.Errorf("value %v of type %T cannot be converted to %s", val, val, t)
	}
}

func ParseDateStringToMillis(v string) (int64, bool) {
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		return i, true
	}
	if t, _, ok := tryParseDateBound(v); ok {
		return t.UnixMilli(), true
	}
	return 0, false
}

func parseDateBound(val string) (time.Time, error) {
	if t, _, ok := tryParseDateBound(val); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date bound: %q", val)
}

func tryParseFastDate(val string) (time.Time, string, bool) {
	if len(val) == 10 && val[4] == '-' && val[7] == '-' {
		y, err1 := strconv.Atoi(val[0:4])
		m, err2 := strconv.Atoi(val[5:7])
		d, err3 := strconv.Atoi(val[8:10])
		if err1 == nil && err2 == nil && err3 == nil {
			t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
			if t.Year() == y && int(t.Month()) == m && t.Day() == d {
				return t, time.DateOnly, true
			}
		}
	}
	if len(val) == 19 && val[4] == '-' && val[7] == '-' && val[10] == ' ' && val[13] == ':' && val[16] == ':' {
		y, err1 := strconv.Atoi(val[0:4])
		m, err2 := strconv.Atoi(val[5:7])
		d, err3 := strconv.Atoi(val[8:10])
		h, err4 := strconv.Atoi(val[11:13])
		mins, err5 := strconv.Atoi(val[14:16])
		sec, err6 := strconv.Atoi(val[17:19])
		if err1 == nil && err2 == nil && err3 == nil && err4 == nil && err5 == nil && err6 == nil {
			t := time.Date(y, time.Month(m), d, h, mins, sec, 0, time.UTC)
			if t.Year() == y && int(t.Month()) == m && t.Day() == d && t.Hour() == h && t.Minute() == mins && t.Second() == sec {
				return t, time.DateTime, true
			}
		}
	}
	return time.Time{}, "", false
}

func tryParseDateBound(val string) (time.Time, string, bool) {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}, "", false
	}
	if t, f, ok := tryParseFastDate(val); ok {
		return t, f, true
	}
	if len(val) > 19 && val[10] == ' ' {
		if t, err := time.Parse(time.DateTime, val); err == nil {
			return t.UTC(), time.DateTime, true
		}
	}
	if t, err := time.Parse(time.RFC3339, val); err == nil {
		return t, time.RFC3339, true
	}
	if t, f, ok := tryParseOffsetBound(val); ok {
		return t, f, true
	}
	return time.Time{}, "", false
}

func tryParseOffsetBound(val string) (time.Time, string, bool) {
	if matches := offsetRegex.FindStringSubmatch(val); matches != nil {
		sign := matches[1]
		num, err := strconv.Atoi(matches[2])
		if err != nil {
			return time.Time{}, "", false
		}
		unit := strings.ToLower(matches[3])
		if sign == "-" {
			num = -num
		}
		now := time.Now().Truncate(24 * time.Hour)
		switch unit {
		case "d":
			return now.AddDate(0, 0, num), time.DateOnly, true
		case "w":
			return now.AddDate(0, 0, num*7), time.DateOnly, true
		case "m":
			return now.AddDate(0, num, 0), time.DateOnly, true
		case "y":
			return now.AddDate(num, 0, 0), time.DateOnly, true
		}
	}
	return time.Time{}, "", false
}

func parseDateOrDynamicBound(val string, setDynamic func(ref, sign string, num int, unit string)) (time.Time, error) {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "@") {
		if matches := dynamicDateRefRegex.FindStringSubmatch(val); matches != nil {
			ref := matches[1]
			sign := matches[2]
			num := 0
			if matches[3] != "" {
				var err error
				num, err = strconv.Atoi(matches[3])
				if err != nil {
					return time.Time{}, fmt.Errorf("invalid offset number in dynamic date reference %q: %w", val, err)
				}
			}
			unit := strings.ToLower(matches[4])
			setDynamic(ref, sign, num, unit)
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("invalid dynamic date reference: %q", val)
	}
	return parseDateBound(val)
}

func applyDateOffset(base time.Time, sign string, num int, unit string) time.Time {
	if sign == "-" {
		num = -num
	}
	switch strings.ToLower(unit) {
	case "d":
		return base.AddDate(0, 0, num)
	case "w":
		return base.AddDate(0, 0, num*7)
	case "m":
		return base.AddDate(0, num, 0)
	case "y":
		return base.AddDate(num, 0, 0)
	default:
		return base
	}
}

func resolveRowDate(val any) (time.Time, bool) {
	if val == nil {
		return time.Time{}, false
	}
	switch v := val.(type) {
	case int64:
		return time.UnixMilli(v).UTC(), true
	case string:
		if t, _, ok := tryParseDateBound(v); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseBoundInt64(val any) (int64, error) {
	switch v := val.(type) {
	case float64:
		if v >= 9223372036854775808.0 || v < -9223372036854775808.0 || math.IsNaN(v) {
			return 0, fmt.Errorf("float64 bound out of int64 bounds: %v", v)
		}
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, nil
		}
		return 0, fmt.Errorf("invalid json.Number for int64 bound: %v", v)
	case string:
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i, nil
		}
		return 0, fmt.Errorf("invalid string for int64 bound: %v", v)
	}
	return 0, fmt.Errorf("invalid type for int64 bound: %T", val)
}

func parseBoundFloat64(val any) (float64, error) {
	var f float64
	switch v := val.(type) {
	case float64:
		f = v
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		var err error
		if f, err = v.Float64(); err != nil {
			return 0, fmt.Errorf("invalid json.Number for float64 bound: %v", v)
		}
	case string:
		var err error
		if f, err = strconv.ParseFloat(v, 64); err != nil {
			return 0, fmt.Errorf("invalid string for float64 bound: %v", v)
		}
	default:
		return 0, fmt.Errorf("invalid type for float64 bound: %T", val)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("float64 bound %v must be finite", val)
	}
	return f, nil
}

func (g *Generator) int64Range(minVal, maxVal int64) int64 {
	if minVal >= maxVal {
		return minVal
	}
	diff := uint64(maxVal) - uint64(minVal) + 1
	if diff == 0 {
		return int64(g.faker.Uint64())
	}
	maxUint := ^uint64(0)
	limit := maxUint - (maxUint % diff)
	for {
		v := g.faker.Uint64()
		if v < limit {
			return minVal + int64(v%diff)
		}
	}
}

func (g *Generator) generateMockString(method string) string {
	switch method {
	case "first_name":
		return g.faker.FirstName()
	case "last_name":
		return g.faker.LastName()
	case "city_name":
		return g.faker.City()
	case "state_code":
		return g.faker.StateAbr()
	case "company_name":
		return g.faker.Company()
	case "zip5_code":
		var buf [5]byte
		writeDigits(buf[:], g.faker.IntRange(10000, 99999))
		return string(buf[:])
	case "phone_number":
		var buf [10]byte
		writeDigits(buf[0:3], g.faker.IntRange(200, 999))
		writeDigits(buf[3:6], g.faker.IntRange(200, 999))
		writeDigits(buf[6:10], g.faker.IntRange(0, 9999))
		return string(buf[:])
	case "street_address":
		return g.faker.Street()
	case "email_address":
		return g.faker.Email()
	case "ssn":
		var buf [11]byte
		writeDigits(buf[0:3], g.faker.IntRange(900, 999))
		buf[3] = '-'
		writeDigits(buf[4:6], g.faker.IntRange(1, 99))
		buf[6] = '-'
		writeDigits(buf[7:11], g.faker.IntRange(1, 9999))
		return string(buf[:])
	}
	return ""
}
