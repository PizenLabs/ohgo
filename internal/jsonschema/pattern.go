package jsonschema

import "regexp"

// patternCache memoises compiled patterns, since the same tool schema is
// validated on every call.
var patternCache = map[string]*regexp.Regexp{}

// matchPattern reports whether value matches a JSON Schema pattern.
//
// JSON Schema patterns are unanchored ECMA-262 regular expressions; Go's
// regexp is close enough for the patterns tool schemas use, and matching is
// unanchored by default in both engines.
func matchPattern(pattern, value string) (bool, error) {
	compiled, ok := patternCache[pattern]
	if !ok {
		var err error
		compiled, err = regexp.Compile(pattern)
		if err != nil {
			return false, err
		}
		patternCache[pattern] = compiled
	}
	return compiled.MatchString(value), nil
}
