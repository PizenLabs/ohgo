package tools

import (
	"net/url"
	"strings"

	"github.com/PizenLabs/ohgo/knowledge"
)

// browserLink is one link discovered in the current page snapshot.
type browserLink struct {
	// Index is the 1-based position used by the follow operation.
	Index int `json:"index"`
	// URL is the absolute target.
	URL string `json:"url"`
	// Text is the bounded anchor text.
	Text string `json:"text,omitempty"`
	// Allowed reports whether the session's host policy permits following it.
	Allowed bool `json:"allowed"`
}

// pageTitle extracts and bounds the document title.
func pageTitle(html string) string {
	lower := strings.ToLower(html)
	start := strings.Index(lower, "<title")
	if start < 0 {
		return ""
	}
	open := strings.Index(html[start:], ">")
	if open < 0 {
		return ""
	}
	contentStart := start + open + 1
	end := strings.Index(lower[contentStart:], "</title>")
	if end < 0 {
		return ""
	}
	title, _ := knowledge.BoundText(strings.TrimSpace(htmlToText(html[contentStart:contentStart+end])), 200)
	return title
}

// extractLinks scans anchors from an HTML document without building a DOM.
//
// It is deliberately small and conservative: a malformed anchor is skipped,
// only absolute http/https targets are returned, and every value is bounded.
// Link discovery is data extraction, never navigation authority.
func extractLinks(html string, base *url.URL, max int, allowed func(host string) bool) []browserLink {
	if max <= 0 {
		return nil
	}
	var links []browserLink
	seen := map[string]bool{}
	lower := strings.ToLower(html)
	for offset := 0; offset < len(html) && len(links) < max; {
		start := strings.Index(lower[offset:], "<a")
		if start < 0 {
			break
		}
		start += offset
		// Reject a tag whose name merely starts with "a" (for example <abbr>).
		if start+2 < len(html) {
			next := html[start+2]
			if next != '>' && next != ' ' && next != '\t' && next != '\n' && next != '\r' && next != '/' {
				offset = start + 2
				continue
			}
		}
		tagEnd := findTagEnd(html, start)
		if tagEnd < 0 {
			break
		}
		href, ok := anchorHref(html[start+1 : tagEnd])
		if !ok {
			offset = tagEnd + 1
			continue
		}
		closeStart := strings.Index(strings.ToLower(html[tagEnd+1:]), "</a>")
		anchorText := ""
		next := tagEnd + 1
		if closeStart >= 0 {
			end := tagEnd + 1 + closeStart
			anchorText, _ = knowledge.BoundText(strings.TrimSpace(htmlToText(html[tagEnd+1:end])), 120)
			next = end + 4
		}
		offset = next
		target, err := base.Parse(strings.TrimSpace(href))
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil {
			continue
		}
		target.Fragment = ""
		absolute := target.String()
		if len(absolute) > 2048 {
			continue
		}
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		links = append(links, browserLink{
			Index:   len(links) + 1,
			URL:     absolute,
			Text:    anchorText,
			Allowed: allowed(target.Hostname()),
		})
	}
	return links
}

// findTagEnd returns the index of the '>' that closes a tag, honouring quoted
// attribute values.
func findTagEnd(html string, start int) int {
	var quote byte
	for i := start + 1; i < len(html); i++ {
		c := html[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			return i
		}
	}
	return -1
}

// anchorHref extracts the href attribute from the inside of an anchor tag.
func anchorHref(tag string) (string, bool) {
	// Drop the tag name.
	tag = strings.TrimSpace(tag)
	if len(tag) < 1 || strings.ToLower(tag[:1]) != "a" {
		return "", false
	}
	i := 1
	for i < len(tag) {
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n' || tag[i] == '\r' || tag[i] == '/') {
			i++
		}
		nameStart := i
		for i < len(tag) && tag[i] != '=' && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\n' && tag[i] != '\r' && tag[i] != '>' {
			i++
		}
		name := strings.ToLower(tag[nameStart:i])
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n' || tag[i] == '\r') {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			continue
		}
		i++
		for i < len(tag) && (tag[i] == ' ' || tag[i] == '\t' || tag[i] == '\n' || tag[i] == '\r') {
			i++
		}
		if i >= len(tag) {
			return "", false
		}
		value := ""
		if tag[i] == '"' || tag[i] == '\'' {
			quote := tag[i]
			i++
			valueStart := i
			for i < len(tag) && tag[i] != quote {
				i++
			}
			value = tag[valueStart:i]
			if i < len(tag) {
				i++
			}
		} else {
			valueStart := i
			for i < len(tag) && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\n' && tag[i] != '\r' && tag[i] != '>' {
				i++
			}
			value = tag[valueStart:i]
		}
		if name == "href" {
			return value, true
		}
	}
	return "", false
}
