package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// openExternalURL hands a validated HTTPS page to the platform browser.
//
// It starts the handler and returns without waiting: a browser may outlive
// ohgo, and a blocking wait would freeze the update card behind a GUI launch.
// The scheme check lives here so every caller inherits the same boundary.
func openExternalURL(target string) error {
	cmd, err := browserCommand(target)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", target, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// browserCommand builds the platform handler invocation without running it.
//
// Only absolute HTTPS URLs are accepted. A release link is a display surface,
// not a hook for arbitrary schemes or local paths.
func browserCommand(target string) (*exec.Cmd, error) {
	target = strings.TrimSpace(target)
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("open: refusing %q, want an absolute https URL", target)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target), nil
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target), nil
	default:
		return exec.Command("xdg-open", target), nil
	}
}
