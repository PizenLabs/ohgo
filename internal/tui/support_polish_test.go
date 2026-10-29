package tui

import (
	"strings"
	"testing"
)

// TestSupportModalResponsiveClamp pins the viewport formula:
// width = clamp(60, w-12, 90), height = clamp(16, h-6, 40).
func TestSupportModalResponsiveClamp(t *testing.T) {
	if rect := SupportModalRect(100, 30); rect.Width != 88 || rect.Height != 24 {
		t.Fatalf("100x30 rect = %+v, want 88x24", rect)
	}
	if rect := SupportModalRect(200, 60); rect.Width != 90 || rect.Height != 40 {
		t.Fatalf("large rect = %+v, want capped 90x40", rect)
	}
	for _, size := range [][2]int{{70, 20}, {50, 15}, {40, 10}, {30, 8}} {
		rect := SupportModalRect(size[0], size[1])
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.Width > size[0] || rect.Y+rect.Height > size[1] {
			t.Errorf("size %v: rect %+v escapes the terminal", size, rect)
		}
	}
}

// TestSupportInlineQueryLine proves the label and prompt share one scannable
// row on the shared left axis instead of stacking.
func TestSupportInlineQueryLine(t *testing.T) {
	s := openCompletedSupport(t, "plain answer")
	out := stripANSI(Render(s))
	found := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimRight(line, " ")
		if strings.Contains(trimmed, "QUERY") && strings.Contains(trimmed, "what is java?") {
			found = true
			if strings.Count(trimmed, "QUERY") != 1 {
				t.Error("query label repeats on the inline row")
			}
			if !strings.Contains(trimmed, "›") {
				t.Error("inline query row lost its › separator")
			}
		}
	}
	if !found {
		t.Fatal("no single row holds QUERY and the question together")
	}
}

// TestSupportUnifiedLeftAxis proves every modal body row starts on the same
// two-cell margin.
func TestSupportUnifiedLeftAxis(t *testing.T) {
	s := openCompletedSupport(t, "plain answer")
	out := stripANSI(Render(s))
	inModal := false
	checked := 0
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimRight(line, " ")
		if strings.Contains(trimmed, "SUPPORT QUERY") {
			inModal = true
			continue
		}
		if !inModal {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(trimmed), "└") {
			break
		}
		// Modal content rows are bordered; the interior must open with the axis.
		inner := trimmed
		if i := strings.Index(inner, "│"); i >= 0 {
			inner = inner[i+len("│"):]
			if strings.HasPrefix(inner, "─") || strings.HasPrefix(inner, "┌") {
				continue
			}
			if !strings.HasPrefix(inner, "  ") {
				t.Errorf("modal row off the shared axis: %q", trimmed)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("found no modal body rows to check")
	}
}

// TestSupportAnswerIndicatorRightAligned proves the scroll count docks at
// the right edge of the ANSWER header instead of trailing the label.
func TestSupportAnswerIndicatorRightAligned(t *testing.T) {
	s := openCompletedSupport(t, longSupportAnswer())
	out := stripANSI(Render(s))
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "ANSWER") && strings.Contains(line, "↑") {
			found = true
			trimmed := strings.TrimRight(line, " ")
			idx := strings.LastIndex(trimmed, "↑")
			if idx < len(trimmed)/2 {
				t.Errorf("scroll indicator sits mid-row instead of right-aligned: %q", trimmed)
			}
		}
	}
	if !found {
		t.Fatal("long answer shows no right-aligned ANSWER indicator")
	}
}

// TestSupportViewportYielding proves the answer keeps roughly three
// quarters of the body on a roomy terminal.
func TestSupportViewportYielding(t *testing.T) {
	s := openCompletedSupport(t, longSupportAnswer())
	rect := SupportModalRect(100, 30)
	interior := rect.Width - 2
	contentH := rect.Height - 3
	body := supportModalLines(s.SupportPanel, PaletteFor(s.Theme), interior, contentH, s)
	if len(body) != contentH {
		t.Fatalf("body rows = %d, want %d", len(body), contentH)
	}
	// Fixed chrome: query(1) + divider + header + divider + input = 5.
	answerRows := len(body) - 5
	if float64(answerRows)/float64(len(body)) < 0.7 {
		t.Fatalf("answer rows = %d of %d, want >=70%% of the body", answerRows, len(body))
	}
}

// TestSupportHighlightKeepsMainChatPlain proves entity highlighting lives
// only in the $sup renderer: main-chat parsing stays untouched.
func TestSupportHighlightKeepsMainChatPlain(t *testing.T) {
	pal := DarkPalette()
	plain := parseInline("Golang by Google in 2007", pal)
	for _, c := range plain {
		if c.style != nil {
			t.Fatal("main-chat inline parsing gained overlay styling")
		}
	}
	hi := highlightSupportChunks(plain, pal)
	styled := 0
	for _, c := range hi {
		if c.style != nil {
			styled++
		}
	}
	if styled == 0 {
		t.Fatal("support renderer highlights no key entities")
	}
	if !isSupportSectionHeader("Origins and Design:") {
		t.Fatal("trailing-colon section header not detected")
	}
}

// TestSupportCompactFooter proves the bottom zone collapsed to the input/
// status row plus one hint row.
func TestSupportCompactFooter(t *testing.T) {
	s := openCompletedSupport(t, "plain answer")
	out := stripANSI(Render(s))
	if !strings.Contains(out, "Esc close") || !strings.Contains(out, "Enter ask") {
		t.Fatal("compact footer lost its close/ask hints")
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, ">") && strings.Contains(line, "openrouter/cohere") && strings.Contains(line, "done") {
			found = true
		}
	}
	if !found {
		t.Fatal("no single row holds the composer, model, and done status together")
	}
}
