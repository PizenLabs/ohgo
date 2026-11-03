package main

import "testing"

// The card's link is a bounded display surface: a non-HTTPS or relative target
// must never reach a platform handler.
func TestBrowserCommandRejectsNonHTTPSURLs(t *testing.T) {
	for _, target := range []string{
		"",
		"github.com/PizenLabs/ohgo/releases",
		"http://github.com/PizenLabs/ohgo/releases",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"https://",
	} {
		if cmd, err := browserCommand(target); err == nil {
			t.Errorf("browserCommand(%q) accepted %v", target, cmd.Args)
		}
	}
}

func TestBrowserCommandAcceptsAbsoluteHTTPS(t *testing.T) {
	cmd, err := browserCommand("https://github.com/PizenLabs/ohgo/releases/")
	if err != nil {
		t.Fatalf("browserCommand: %v", err)
	}
	if cmd == nil || len(cmd.Args) < 2 {
		t.Fatalf("browserCommand returned %+v", cmd)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != "https://github.com/PizenLabs/ohgo/releases/" {
		t.Fatalf("browserCommand target = %q", got)
	}
}
