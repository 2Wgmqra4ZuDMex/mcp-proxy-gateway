package toolsearch

import (
	"strings"
	"unicode"
)

// Tokenize splits tool-oriented identifiers and natural-language text into
// lower-cased tokens. It understands common identifier separators, camel case,
// acronym boundaries, letter/number boundaries, and Chinese text (segmented into
// words with an embedded common-word dictionary).
func Tokenize(s string) []string {
	return tokenizeWith(s, 0, nil)
}

// tokenize applies the same token order as Tokenize while allowing internal
// callers to stop as soon as a fixed token budget is reached. Description
// indexing uses it to avoid materializing thousands of CJK bigrams that will
// be discarded by descTokenLimit.
func tokenize(s string, maxTokens int) []string {
	return tokenizeWith(s, maxTokens, nil)
}

// tokenizeWith 与 tokenize 相同，extra 为本次索引补充的中文词（AI 生成的关键词）。
func tokenizeWith(s string, maxTokens int, extra map[string]struct{}) []string {
	runes := []rune(s)
	capacity := len(runes)
	if maxTokens > 0 && capacity > maxTokens {
		capacity = maxTokens
	}
	out := make([]string, 0, capacity)
	word := make([]rune, 0, 32)

	flushWord := func() bool {
		if len(word) == 0 {
			return false
		}
		out = append(out, strings.ToLower(string(word)))
		word = word[:0]
		return maxTokens > 0 && len(out) >= maxTokens
	}

	for i := 0; i < len(runes); {
		if maxTokens > 0 && len(out) >= maxTokens {
			return out
		}
		r := runes[i]
		if unicode.Is(unicode.Han, r) {
			if flushWord() {
				return out
			}
			start := i
			for i < len(runes) && unicode.Is(unicode.Han, runes[i]) {
				i++
			}
			for _, word := range hanTokens(runes[start:i], extra) {
				out = append(out, word)
				if maxTokens > 0 && len(out) >= maxTokens {
					return out
				}
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			if flushWord() {
				return out
			}
			i++
			continue
		}

		if len(word) > 0 {
			prev := runes[i-1]
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			boundary := (unicode.IsLetter(prev) && unicode.IsDigit(r)) ||
				(unicode.IsDigit(prev) && unicode.IsLetter(r)) ||
				(unicode.IsLower(prev) && unicode.IsUpper(r)) ||
				(unicode.IsUpper(prev) && unicode.IsUpper(r) && unicode.IsLower(next))
			if boundary {
				if flushWord() {
					return out
				}
			}
		}
		word = append(word, r)
		i++
	}
	flushWord()
	return out
}

func normalizeQuery(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func dedupeKeepOrder(tokens []string) []string {
	if len(tokens) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func isCJK(token string) bool {
	for _, r := range token {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
