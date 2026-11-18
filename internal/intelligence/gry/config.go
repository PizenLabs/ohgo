package gry

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults bound one structural query. They are deliberately small: the
// provider exists to reduce model context, so an unbounded graph dump is a
// failure of the integration, not a feature.
const (
	DefaultBinary    = "gry"
	DefaultTimeout   = 2 * time.Second
	DefaultMaxTokens = 3200
	DefaultMaxNodes  = 40
	DefaultMaxRows   = 40
	DefaultMaxDepth  = 2
	DefaultMaxOutput = 512 * 1024
	maxStderrBytes   = 4096
	projectCacheTTL  = 30 * time.Second
)

// Config is the external structural-intelligence contract.
//
// It is resolved from the environment, never from model output. An empty root,
// a disabled flag, or a missing binary makes the provider unavailable; Ohgo then
// uses its native structural path. Nothing here triggers indexing.
type Config struct {
	// Root is the workspace whose project identity is resolved. gry owns the
	// project name; Ohgo only asks which indexed project has this root.
	Root string
	// Binary is the gry executable or its PATH name.
	Binary string
	// Project pins the gry project identity. Empty means resolve it from Root.
	Project string
	// CacheDir overrides GRY_CACHE_DIR for the child process.
	CacheDir string
	// Timeout bounds one command.
	Timeout time.Duration
	// MaxTokens bounds gry's context token budget.
	MaxTokens int
	// MaxNodes bounds the context node set.
	MaxNodes int
	// MaxRows bounds trace, search, and change rows.
	MaxRows int
	// MaxDepth bounds relationship traversal.
	MaxDepth int
	// MaxOutputBytes bounds the raw JSON read from the child. Raw output is
	// parsed, not forwarded to the model.
	MaxOutputBytes int
	// Disabled is the explicit opt-out. It never disables Ohgo's native path.
	Disabled bool
}

// FromEnv reads the narrow, opt-out integration contract.
//
// The provider is enabled only when a binary can be found at request time. An
// absent gry installation is not an error and never blocks Ohgo startup.
func FromEnv(root string) Config {
	cfg := Config{
		Root:      root,
		Binary:    strings.TrimSpace(os.Getenv("OHGO_GRY_BIN")),
		Project:   strings.TrimSpace(os.Getenv("OHGO_GRY_PROJECT")),
		CacheDir:  strings.TrimSpace(os.Getenv("OHGO_GRY_CACHE_DIR")),
		Timeout:   DefaultTimeout,
		MaxTokens: DefaultMaxTokens,
		MaxNodes:  DefaultMaxNodes,
		MaxRows:   DefaultMaxRows,
		MaxDepth:  DefaultMaxDepth,
	}
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	cfg.Disabled = explicitlyDisabled(os.Getenv("OHGO_GRY"))
	cfg.Timeout = envDuration("OHGO_GRY_TIMEOUT_MS", cfg.Timeout, 100*time.Millisecond, 30*time.Second)
	cfg.MaxTokens = envInt("OHGO_GRY_MAX_TOKENS", cfg.MaxTokens, 256, 32000)
	cfg.MaxNodes = envInt("OHGO_GRY_MAX_NODES", cfg.MaxNodes, 8, 200)
	cfg.MaxRows = envInt("OHGO_GRY_MAX_ROWS", cfg.MaxRows, 4, 200)
	cfg.MaxDepth = envInt("OHGO_GRY_MAX_DEPTH", cfg.MaxDepth, 1, 5)
	return cfg
}

func explicitlyDisabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off", "disabled":
		return true
	}
	return false
}

func envInt(name string, fallback, low, high int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func envDuration(name string, fallback, low, high time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	value := time.Duration(ms) * time.Millisecond
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func (c Config) normalized() Config {
	if c.Binary == "" {
		c.Binary = DefaultBinary
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	if c.MaxNodes <= 0 {
		c.MaxNodes = DefaultMaxNodes
	}
	if c.MaxRows <= 0 {
		c.MaxRows = DefaultMaxRows
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = DefaultMaxDepth
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = DefaultMaxOutput
	}
	return c
}
