package generator

import (
	"bytes"
	"strconv"
	"unsafe"

	"github.com/brianvoe/gofakeit/v7"
)

type regexNode struct {
	chars    []byte
	minCount int
	maxCount int
}

// CompileRegex acts as a lightweight regex parser and compiler for data generation.
// It supports basic features like literals, character classes ([a-z0-9]), ranges (A-Z),
// and quantifiers ({N} or {N,M}). If it encounters unsupported advanced regex features,
// it safely returns nil, signaling the caller to fall back to the slower gofakeit.Regex engine.
func CompileRegex(pattern string) func(*gofakeit.Faker) string {
	var nodes []regexNode
	maxLen := 0
	i := 0
	n := len(pattern)
	for i < n {
		chars, next, ok := parseRegexToken(pattern, i)
		if !ok {
			return nil
		}
		i = next
		minCount, maxCount, next, ok := parseQuantifier(pattern, i)
		if !ok {
			return nil
		}
		i = next
		nodes = append(nodes, regexNode{chars: chars, minCount: minCount, maxCount: maxCount})
		maxLen += maxCount
	}
	return func(faker *gofakeit.Faker) string {
		return renderRegex(faker, nodes, maxLen)
	}
}

func parseRegexToken(pattern string, i int) ([]byte, int, bool) {
	n := len(pattern)
	switch {
	case pattern[i] == '\\':
		if i+1 >= n {
			return nil, i, false
		}
		c := pattern[i+1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return nil, i, false
		}
		return []byte{c}, i + 2, true
	case pattern[i] == '[':
		return parseCharClass(pattern, i+1)
	case isUnsupportedRegexChar(pattern[i]):
		return nil, i, false
	default:
		return []byte{pattern[i]}, i + 1, true
	}
}

func isUnsupportedRegexChar(c byte) bool {
	switch c {
	case '(', ')', '|', '*', '+', '?', '.', '^', '$':
		return true
	default:
		return false
	}
}

func parseCharClass(pattern string, i int) ([]byte, int, bool) {
	n := len(pattern)
	if i < n && pattern[i] == '^' {
		return nil, i, false
	}
	var chars []byte
	for i < n {
		if pattern[i] == ']' {
			return chars, i + 1, len(chars) > 0
		}
		if pattern[i] == '\\' {
			return nil, i, false
		}
		if i+2 < n && pattern[i+1] == '-' && pattern[i+2] != ']' {
			for c := pattern[i]; c <= pattern[i+2]; c++ {
				chars = append(chars, c)
			}
			i += 3
		} else {
			chars = append(chars, pattern[i])
			i++
		}
	}
	return nil, i, false
}

func parseQuantifier(pattern string, i int) (int, int, int, bool) {
	n := len(pattern)
	if i >= n || pattern[i] != '{' {
		return 1, 1, i, true
	}
	start := i + 1
	end := bytes.IndexByte([]byte(pattern[start:]), '}')
	if end == -1 {
		return 0, 0, 0, false
	}
	quant := pattern[start : start+end]
	next := start + end + 1
	parts := bytes.Split([]byte(quant), []byte(","))
	switch len(parts) {
	case 1:
		val, err := strconv.Atoi(string(parts[0]))
		if err != nil || val < 0 {
			return 0, 0, 0, false
		}
		return val, val, next, true
	case 2:
		minCount, maxCount := 0, 0
		if len(parts[0]) > 0 {
			val, err := strconv.Atoi(string(parts[0]))
			if err != nil || val < 0 {
				return 0, 0, 0, false
			}
			minCount = val
		}
		if len(parts[1]) == 0 {
			return 0, 0, 0, false
		}
		val, err := strconv.Atoi(string(parts[1]))
		if err != nil || val < 0 || val < minCount {
			return 0, 0, 0, false
		}
		maxCount = val
		return minCount, maxCount, next, true
	default:
		return 0, 0, 0, false
	}
}

func renderRegex(faker *gofakeit.Faker, nodes []regexNode, maxLen int) string {
	if maxLen == 0 {
		return ""
	}
	buf := make([]byte, maxLen)
	pos := 0
	for i := range nodes {
		node := &nodes[i]
		count := node.minCount
		if node.maxCount > node.minCount {
			count += faker.IntRange(0, node.maxCount-node.minCount)
		}
		chars := node.chars
		n := uint32(len(chars))
		if n == 1 {
			c := chars[0]
			for range count {
				buf[pos] = c
				pos++
			}
			continue
		}
		for count >= 4 {
			r := faker.Uint64()
			buf[pos] = chars[(uint32(uint16(r))*n)>>16]
			buf[pos+1] = chars[(uint32(uint16(r>>16))*n)>>16]
			buf[pos+2] = chars[(uint32(uint16(r>>32))*n)>>16]
			buf[pos+3] = chars[(uint32(uint16(r>>48))*n)>>16]
			pos += 4
			count -= 4
		}
		if count > 0 {
			r := faker.Uint64()
			for range count {
				buf[pos] = chars[(uint32(uint16(r))*n)>>16]
				pos++
				r >>= 16
			}
		}
	}
	if pos == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(buf), pos)
}
