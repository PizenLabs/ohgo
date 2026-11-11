package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// System appearance detection.
//
// The stored preference is "auto", "dark", or "light"; only Auto needs the
// host. Detection is deliberately best-effort and bounded: a short-lived,
// timed subprocess at startup and on a slow poll while Auto is active. It never
// runs during a render, and an explicit preference disables it entirely.
//
// There is no universal terminal API for this. macOS exposes the desktop
// appearance through defaults; GNOME and KDE expose theirs through their own
// configuration tools; Windows exposes it through the registry. A remote
// session cannot see the client's desktop at all, so SSH environments return
// unknown and the documented dark fallback applies rather than a false claim.
const (
	appearanceCommandTimeout = 750 * time.Millisecond
	// appearancePollInterval follows a desktop appearance change without
	// shelling out on every frame. Ten seconds is responsive enough for a
	// human toggling their theme while remaining negligible overhead.
	appearancePollInterval = 10 * time.Second
)

// appearanceRunner runs one platform query. It is a field on the detector so
// tests exercise the parsing and fallback path without touching the host.
type appearanceRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// systemAppearance carries the platform facts detection needs.
//
// It is a value rather than a set of globals so tests can substitute the OS,
// environment, and command runner independently and run in parallel.
type systemAppearance struct {
	goos   string
	getenv func(string) string
	run    appearanceRunner
}

// DetectSystemAppearance reports the host's appearance and whether it is
// trustworthy. ok=false means no supported source answered; the caller must
// use the deterministic dark fallback and label it as such.
func DetectSystemAppearance() (theme string, ok bool) {
	return systemAppearance{
		goos:   runtime.GOOS,
		getenv: os.Getenv,
		run:    runAppearanceCommand,
	}.detect()
}

// runAppearanceCommand executes one bounded probe.
func runAppearanceCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (s systemAppearance) commandTimeout() time.Duration {
	return appearanceCommandTimeout
}

// detect routes to the platform probe. A remote shell is unknown by
// construction: the process runs on the server, not on the human's desktop.
func (s systemAppearance) detect() (string, bool) {
	if s.run == nil || s.getenv == nil {
		return "", false
	}
	if s.remoteSession() {
		return "", false
	}
	switch s.goos {
	case "darwin":
		return s.detectDarwin()
	case "linux":
		return s.detectLinux()
	case "windows":
		return s.detectWindows()
	default:
		return "", false
	}
}

// remoteSession reports whether the process is behind SSH. Terminal
// multiplexers such as tmux are not remote: they still run beside the desktop
// and the host preference remains meaningful.
func (s systemAppearance) remoteSession() bool {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if strings.TrimSpace(s.getenv(key)) != "" {
			return true
		}
	}
	return false
}

// detectDarwin reads the global AppleInterfaceStyle default. An absent key is
// the Aqua (light) appearance; a failure that is not an absence is unknown.
func (s systemAppearance) detectDarwin() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), s.commandTimeout())
	defer cancel()
	out, err := s.run(ctx, "defaults", "read", "-g", "AppleInterfaceStyle")
	text := strings.ToLower(strings.TrimSpace(string(out)))
	if err == nil {
		if strings.Contains(text, "dark") {
			return ThemeDark, true
		}
		return ThemeLight, true
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && strings.Contains(text, "does not exist") {
		return ThemeLight, true
	}
	return "", false
}

// detectLinux asks the desktop's own settings first, then falls back to the
// theme name. Headless machines and minimal window managers answer neither.
func (s systemAppearance) detectLinux() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), s.commandTimeout())
	defer cancel()

	if out, err := s.run(ctx, "gsettings", "get", "org.gnome.desktop.interface", "color-scheme"); err == nil {
		switch {
		case strings.Contains(strings.ToLower(string(out)), "prefer-dark"):
			return ThemeDark, true
		case strings.Contains(strings.ToLower(string(out)), "prefer-light"):
			return ThemeLight, true
		case strings.Contains(strings.ToLower(string(out)), "default"):
			// The explicit preference is neutral; the theme name still says
			// which side of the scale is in force.
		}
	}
	if out, err := s.run(ctx, "gsettings", "get", "org.gnome.desktop.interface", "gtk-theme"); err == nil {
		if strings.Contains(strings.ToLower(string(out)), "dark") {
			return ThemeDark, true
		}
		return ThemeLight, true
	}
	for _, tool := range []string{"kreadconfig6", "kreadconfig5"} {
		if out, err := s.run(ctx, tool, "--file", "kdeglobals", "--group", "General", "--key", "ColorScheme"); err == nil {
			if strings.Contains(strings.ToLower(string(out)), "dark") {
				return ThemeDark, true
			}
			return ThemeLight, true
		}
	}
	return "", false
}

// windowsAppsTheme matches the registry line that names the app appearance.
var windowsAppsTheme = regexp.MustCompile(`(?i)AppsUseLightTheme\s+REG_DWORD\s+0x([01])`)

// detectWindows reads the per-user app theme. 0x0 is dark, 0x1 light; a
// malformed answer is unknown rather than a guess.
func (s systemAppearance) detectWindows() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), s.commandTimeout())
	defer cancel()
	out, err := s.run(ctx, "reg", "query",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`,
		"/v", "AppsUseLightTheme")
	if err != nil {
		return "", false
	}
	match := windowsAppsTheme.FindStringSubmatch(string(out))
	if len(match) != 2 {
		return "", false
	}
	if match[1] == "1" {
		return ThemeLight, true
	}
	return ThemeDark, true
}
