// Package promptopt provides deterministic, dependency-free prompt and title
// optimization. It exists so navigation chrome and oversized-prompt offloading
// can remove conversational noise without an extra model call or a third-party
// dependency.
package promptopt

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Local title bounds. A provisional title is navigation chrome, not a summary,
// so it must stay short enough for a roster cell and specific enough to tell
// two sessions apart.
const (
	// TitleMaxWords is the default word bound for a local title.
	TitleMaxWords = 5
	// TitleMaxRunes is the default rune bound for a local title.
	TitleMaxRunes = 40
)

// titleNoise is the pre-filter vocabulary that carries no navigation value.
// Only exact, case-folded words are removed, so identifiers such as "internal"
// or "go" survive even when they contain a stop word.
var titleNoise = wordSet(
	// Articles and determiners.
	"a", "an", "the", "this", "that", "these", "those",
	"my", "your", "our", "their", "its", "some", "any", "each", "every",
	// Pronouns.
	"i", "me", "we", "us", "you", "he", "him", "she", "her", "it",
	"they", "them", "there", "here",
	// Auxiliaries.
	"am", "is", "are", "was", "were", "be", "been", "being",
	"do", "does", "did", "done", "have", "has", "had",
	"can", "could", "may", "might", "must", "shall", "should", "will", "would",
	// Politeness and greetings.
	"please", "kindly", "thanks", "thank", "hello", "hi", "hey", "greetings",
	// Prepositions.
	"to", "of", "in", "on", "at", "by", "for", "with", "from",
	"into", "onto", "about", "as", "via", "per", "over", "under",
	"between", "through", "during", "before", "after", "above", "below",
	"up", "down", "out", "off",
	// Conjunctions.
	"and", "or", "but", "nor", "so", "yet", "because", "while",
	"if", "then", "than", "whether",
	// Interrogatives.
	"what", "which", "who", "whom", "whose", "when", "where", "why", "how",
	// Verbs that only introduce an instruction.
	"use", "using", "used", "inspect", "inspecting", "explain", "explaining",
	"tell", "telling", "want", "wants", "need", "needs", "let", "lets", "let's",
	// Discourse filler.
	"just", "really", "very", "actually", "basically", "simply",
	"maybe", "perhaps", "also", "still", "even", "well", "okay", "ok",
)

// titleEdgePunctuation is decoration that may surround a prompt token but is
// never part of its identity.
const titleEdgePunctuation = "\"'`“”‘’()[]{}<>.,;:!?—–…"

// titleDangling is the subset that may never end a title after truncation.
// Removing it from every position is safe for a title; removing it from the cut
// edge is required even when the caller supplied an already-composed label.
var titleDangling = wordSet(
	"a", "an", "the",
	"to", "of", "in", "on", "at", "by", "for", "with", "from",
	"into", "onto", "about", "as", "via", "per", "over", "under",
	"between", "through", "during", "before", "after", "above", "below",
	"up", "down", "out", "off",
	"and", "or", "but", "nor", "so", "yet", "because", "while",
	"if", "then", "than", "whether",
)

// SanitizeTitle converts a raw prompt into a bounded local title.
//
// It removes conversational noise and articles, collapses repeated words, and
// bounds the result to TitleMaxWords and TitleMaxRunes. Truncation always
// happens at a word boundary, and a trailing article, preposition, or
// conjunction is dropped rather than left dangling. Input made entirely of
// noise returns "" so the caller can render its own placeholder.
func SanitizeTitle(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	words := make([]string, 0, TitleMaxWords)
	seen := make(map[string]struct{}, TitleMaxWords)
	for i := 0; i < len(fields); i++ {
		word := trimTitleToken(fields[i])
		if word == "" {
			continue
		}
		lower := strings.ToLower(word)

		// These phrases survive the per-word filter because their other words
		// ("make", "sure", "advance") are meaningful on their own.
		if (lower == "make" || lower == "be") && nextTitleWord(fields, i) == "sure" {
			i++ // "to", "that", and "the" are already noise words.
			continue
		}
		if lower == "thank" || lower == "thanks" {
			for i+1 < len(fields) {
				switch nextTitleWord(fields, i) {
				case "you", "in", "advance", "so", "much", "a", "lot":
					i++
					continue
				}
				break
			}
			continue
		}

		if _, noise := titleNoise[lower]; noise {
			continue
		}
		if _, duplicate := seen[lower]; duplicate {
			continue
		}
		seen[lower] = struct{}{}
		words = append(words, word)
		if len(words) == TitleMaxWords {
			// More prompt cannot change a word-bounded title; stop before a
			// large pending prompt pays a full token pass on every frame.
			break
		}
	}
	if len(words) == 0 {
		return ""
	}
	return TruncateTitle(strings.Join(words, " "), TitleMaxWords, TitleMaxRunes)
}

// TruncateTitle bounds raw to maxWords words and maxRunes runes without
// splitting a word. A non-positive bound disables that dimension.
//
// If the rune bound would cut inside a word, the whole word is dropped. The one
// exception is a single token longer than maxRunes: it has no whitespace to trim
// back to, so it is capped by runes to keep navigation chrome bounded. Trailing
// articles, prepositions, and conjunctions are removed after both bounds are
// applied, so a cut cannot leave "the", "to", or "and" as the final word.
func TruncateTitle(raw string, maxWords, maxRunes int) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	if maxWords > 0 && len(fields) > maxWords {
		fields = fields[:maxWords]
	}

	if maxRunes <= 0 {
		return strings.Join(dropDanglingTitleWords(fields), " ")
	}

	included := make([]string, 0, len(fields))
	used := 0
	for i, field := range fields {
		n := utf8.RuneCountInString(field)
		need := n
		if i > 0 {
			need++
		}
		if used+need > maxRunes {
			if i == 0 {
				if isDanglingTitleWord(field) {
					return ""
				}
				// A single token longer than the bound has no whitespace to
				// trim back to, so cap it to keep navigation chrome bounded.
				runes := []rune(field)
				return string(runes[:maxRunes])
			}
			break
		}
		if i > 0 {
			used++
		}
		used += n
		included = append(included, field)
	}
	return strings.Join(dropDanglingTitleWords(included), " ")
}

// dropDanglingTitleWords removes trailing words that cannot end a title.
func dropDanglingTitleWords(fields []string) []string {
	for len(fields) > 0 && isDanglingTitleWord(fields[len(fields)-1]) {
		fields = fields[:len(fields)-1]
	}
	return fields
}

// isDanglingTitleWord reports whether word is a stop word that must not be the
// final token. Edge punctuation is ignored so "to," and "to" behave the same.
func isDanglingTitleWord(word string) bool {
	trimmed := strings.ToLower(strings.Trim(word, titleEdgePunctuation))
	_, dangling := titleDangling[trimmed]
	return dangling
}

// trimTitleToken removes decoration around one prompt token while preserving
// path and identifier characters inside it.
func trimTitleToken(field string) string {
	word := strings.TrimFunc(field, func(r rune) bool {
		switch r {
		case '"', '\'', '`', '“', '”', '‘', '’',
			'(', ')', '[', ']', '{', '}', '<', '>',
			'.', ',', ';', ':', '!', '?', '—', '–', '…':
			return true
		}
		return false
	})
	word = strings.TrimRight(word, "/\\")
	if word == "" {
		return ""
	}

	// A heading marker is decoration; "#42" is an identifier and stays intact.
	if strings.HasPrefix(word, "#") {
		if rest := strings.TrimLeft(word, "#"); rest != "" {
			first, _ := utf8.DecodeRuneInString(rest)
			if !unicode.IsDigit(first) {
				word = rest
			}
		}
	}

	// Asterisk emphasis is decoration, but a token containing technical
	// punctuation may be a glob, so only trim the edges of prose-like tokens.
	if !strings.ContainsAny(word, `/\\.?[]{}<>`) {
		word = strings.Trim(word, "*")
	}
	if word == "" || !containsAlphaNumeric(word) {
		return ""
	}
	return word
}

// nextTitleWord returns the trimmed, case-folded word after index i, or "".
func nextTitleWord(fields []string, i int) string {
	if i+1 >= len(fields) {
		return ""
	}
	word := trimTitleToken(fields[i+1])
	if word == "" {
		return ""
	}
	return strings.ToLower(word)
}

// containsAlphaNumeric reports whether word carries content. Pure punctuation
// carries none and must not consume a title slot.
func containsAlphaNumeric(word string) bool {
	for _, r := range word {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// wordSet builds a lookup set from literal words.
func wordSet(words ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(words))
	for _, word := range words {
		set[word] = struct{}{}
	}
	return set
}
