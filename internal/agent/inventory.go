package agent

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// onlyFileClaimPattern matches an exclusive file-count claim. The qualifier is
// the noun immediately before "file" ("Go", "TypeScript", "main.go"). The
// optional words allow "only one real Go file" without accepting an arbitrary
// sentence.
var onlyFileClaimPattern = regexp.MustCompile(
	`(?i)\bonly\s+(one|two|three|four|five|six|seven|eight|nine|ten|\d+)\s+(?:[a-z0-9_.+#-]+\s+){0,3}?([a-z0-9_.+#-]+)\s+files?\b`)

// onlyFileScopePattern captures a path scope stated in the same sentence.
var onlyFileScopePattern = regexp.MustCompile(
	`(?i)\b(?:in|under|inside|within)\s+([a-z0-9_./-]+)`)

// inventorySentenceBoundary splits on sentence punctuation only when it is
// followed by whitespace or the end. A period inside "main.go" or "v1.2" is
// part of the name, not a sentence boundary.
var inventorySentenceBoundary = regexp.MustCompile(`[.!?]+(?:\s+|$)`)

var wordFileCounts = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
}

// inventoryClaimIssue checks an exclusive file-count claim against the files
// the run actually observed. A broad repository analysis that says "only one
// Go file exists" cannot be completed when the run's own glob or listing
// returned several Go files: the answer contradicts its evidence.
//
// The check is deliberately narrow. It only runs for a broad repository
// account, only for known file kinds, and only when the observed count is
// greater than the claimed count. An unsupported claim with no observed files
// is left to the grounding/provenance gates rather than guessed at.
func inventoryClaimIssue(root, answer string, turns []Turn) string {
	for _, sentence := range inventorySentences(answer) {
		match := onlyFileClaimPattern.FindStringSubmatch(sentence)
		if match == nil {
			continue
		}
		claimed := claimedFileCount(match[1])
		if claimed <= 0 {
			continue
		}
		extensions := fileKindExtensions(match[2])
		if len(extensions) == 0 {
			continue
		}
		scope := ""
		if scopeMatch := onlyFileScopePattern.FindStringSubmatch(sentence); scopeMatch != nil {
			scope = normalizeInventoryScope(scopeMatch[1])
		}
		files := observedFileInventory(root, turns)
		if scope != "" && !inventoryHasScope(files, scope) {
			// "in the repository" or another prose phrase is not a path scope.
			scope = ""
		}
		count, examples := countInventoryFiles(files, extensions, scope)
		if count <= claimed {
			continue
		}
		return fmt.Sprintf(
			"the answer claims only %d %s file(s), but the run observed %d%s: %s",
			claimed, strings.TrimSpace(match[2]), count, scopeSuffix(scope),
			strings.Join(examples, ", "))
	}
	return ""
}

// inventorySentences splits an answer into sentence-sized units without
// breaking a dotted file name or version.
func inventorySentences(text string) []string {
	var out []string
	for _, sentence := range inventorySentenceBoundary.Split(text, -1) {
		if sentence = strings.TrimSpace(sentence); sentence != "" {
			out = append(out, sentence)
		}
	}
	return out
}

// claimedFileCount parses a word or decimal count.
func claimedFileCount(value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	if count, ok := wordFileCounts[value]; ok {
		return count
	}
	count, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return count
}

// fileKindExtensions maps the claim's noun to the extensions it identifies.
func fileKindExtensions(kind string) []string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if strings.HasPrefix(kind, ".") {
		return []string{kind}
	}
	if strings.Contains(kind, ".") {
		// A qualified name such as "main.go" or "README.md" identifies a
		// basename, not every file with that extension.
		return []string{"=" + kind}
	}
	switch kind {
	case "go", "golang":
		return []string{".go"}
	case "typescript", "ts", "tsx":
		return []string{".ts", ".tsx"}
	case "javascript", "js", "jsx":
		return []string{".js", ".jsx"}
	case "python", "py":
		return []string{".py"}
	case "rust", "rs":
		return []string{".rs"}
	case "java":
		return []string{".java"}
	case "json":
		return []string{".json"}
	case "yaml", "yml":
		return []string{".yaml", ".yml"}
	case "markdown", "md":
		return []string{".md"}
	case "sql":
		return []string{".sql"}
	case "proto", "protobuf":
		return []string{".proto"}
	case "dockerfile":
		return []string{"Dockerfile"}
	case "makefile":
		return []string{"Makefile"}
	default:
		return nil
	}
}

// observedFileInventory collects every file path the run observed through a
// read, a glob, or a directory listing. It is a display-independent projection
// of the same tool outcomes completion already inspects.
func observedFileInventory(root string, turns []Turn) map[string]bool {
	files := map[string]bool{}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				continue
			}
			base := argumentPath(outcome.Args)
			if looksLikeInventoryFile(base) {
				files[normalizeInventoryPath(root, base)] = true
			}
			if details, ok := outcome.Result.Details.(map[string]any); ok {
				if detailPath, ok := details["path"].(string); ok && looksLikeInventoryFile(detailPath) {
					files[normalizeInventoryPath(root, detailPath)] = true
				}
			}
			switch outcome.ToolName {
			case "glob":
				for _, line := range strings.Split(outcome.Result.Text, "\n") {
					line = strings.TrimSpace(line)
					if looksLikeInventoryFile(line) {
						files[normalizeInventoryPath(root, line)] = true
					}
				}
			case "ls":
				if base == "" {
					continue
				}
				for _, line := range strings.Split(outcome.Result.Text, "\n") {
					name := strings.TrimSpace(line)
					if name == "" || strings.HasSuffix(name, "/") || !looksLikeInventoryFile(name) {
						continue
					}
					files[normalizeInventoryPath(root, filepath.ToSlash(filepath.Join(base, name)))] = true
				}
			}
		}
	}
	return files
}

// argumentPath returns the common path argument of a call.
func argumentPath(args map[string]any) string {
	for _, key := range []string{"path", "file"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// looksLikeInventoryFile reports whether a line names a file rather than a
// directory or a summary.
func looksLikeInventoryFile(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t") {
		return false
	}
	if strings.HasSuffix(value, "/") || strings.HasPrefix(value, "No files matched") {
		return false
	}
	base := path.Base(value)
	ext := path.Ext(base)
	return len(ext) > 1 && len(ext) <= 8
}

// normalizeInventoryPath makes an observed path relative to the workspace root
// and slash-normalised, so an absolute read and a relative glob describe one
// file rather than two.
func normalizeInventoryPath(root, value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	root = filepath.ToSlash(strings.TrimSpace(root))
	if root != "" {
		root = strings.TrimSuffix(root, "/")
		if strings.HasPrefix(value, root+"/") {
			value = strings.TrimPrefix(value, root+"/")
		}
	}
	return value
}

func normalizeInventoryScope(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	return strings.Trim(value, "/")
}

func inventoryHasScope(files map[string]bool, scope string) bool {
	for file := range files {
		if file == scope || strings.HasPrefix(file, scope+"/") {
			return true
		}
	}
	return false
}

func countInventoryFiles(files map[string]bool, extensions []string, scope string) (int, []string) {
	var matches []string
	for file := range files {
		if scope != "" && file != scope && !strings.HasPrefix(file, scope+"/") {
			continue
		}
		base := path.Base(file)
		for _, extension := range extensions {
			switch {
			case strings.HasPrefix(extension, "="):
				if strings.EqualFold(base, strings.TrimPrefix(extension, "=")) {
					matches = append(matches, file)
				}
			case strings.HasPrefix(extension, "."):
				if strings.HasSuffix(strings.ToLower(base), extension) {
					matches = append(matches, file)
				}
			default:
				if strings.EqualFold(base, extension) {
					matches = append(matches, file)
				}
			}
		}
	}
	sort.Strings(matches)
	count := len(matches)
	if len(matches) > 3 {
		matches = matches[:3]
	}
	return count, matches
}

func scopeSuffix(scope string) string {
	if scope == "" {
		return ""
	}
	return " under " + scope
}
