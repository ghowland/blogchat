package main

import (
	"strings"
	"unicode"
)

// maxLinkText is the display length of a link. A longer URL is shortened,
// but the href keeps the whole address.
const maxLinkText = 60

// Segment is one part of a rendered body. A segment is plain text or a
// link, never both. The template escapes Text and URL with the normal
// double-brace form, so no unescaped value ever reaches the page.
type Segment struct {
	Text   string
	URL    string
	IsLink bool
}

// Linkify splits a body into text and link segments. Only http and https
// addresses become links, so no other scheme can reach an href attribute.
func Linkify(body string) []Segment {
	list := make([]Segment, 0, 4)
	rest := body

	for {
		start := findScheme(rest)
		if start < 0 {
			break
		}

		stop := start
		for stop < len(rest) && !isBreak(rune(rest[stop])) {
			stop++
		}
		link := trimTrailing(rest[start:stop])

		// A scheme with nothing after it is not an address.
		if !hasHost(link) {
			list = appendText(list, rest[:stop])
			rest = rest[stop:]
			continue
		}

		if start > 0 {
			list = appendText(list, rest[:start])
		}
		list = append(list, Segment{
			Text:   shortenLink(link),
			URL:    link,
			IsLink: true,
		})
		rest = rest[start+len(link):]
	}

	if rest != "" {
		list = appendText(list, rest)
	}
	return list
}

// findScheme returns the offset of the next http or https address, or -1.
// The address must start at the beginning of the text or after a character
// that cannot be part of a word, so that a scheme inside a longer word is
// not treated as an address.
func findScheme(text string) int {
	offset := 0
	for {
		idx := strings.Index(text[offset:], "http")
		if idx < 0 {
			return -1
		}
		pos := offset + idx

		if pos > 0 && !isBreak(rune(text[pos-1])) {
			offset = pos + 4
			continue
		}
		if strings.HasPrefix(text[pos:], "http://") ||
			strings.HasPrefix(text[pos:], "https://") {
			return pos
		}
		offset = pos + 4
	}
}

// isBreak reports whether a character ends an address.
func isBreak(chr rune) bool {
	return unicode.IsSpace(chr) || chr == '<' || chr == '>' ||
		chr == '"' || chr == '\''
}

// hasHost reports whether anything follows the scheme separator.
func hasHost(link string) bool {
	idx := strings.Index(link, "://")
	return idx >= 0 && len(link) > idx+3
}

// trimTrailing removes punctuation that ends a sentence rather than the
// address. A closing bracket stays when the address opened it, so that a
// link with brackets in its path survives.
func trimTrailing(link string) string {
	for len(link) > 0 {
		last := link[len(link)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?':
			link = link[:len(link)-1]
		case ')':
			if strings.Count(link, "(") >= strings.Count(link, ")") {
				return link
			}
			link = link[:len(link)-1]
		case ']':
			if strings.Count(link, "[") >= strings.Count(link, "]") {
				return link
			}
			link = link[:len(link)-1]
		default:
			return link
		}
	}
	return link
}

// shortenLink makes the display text. A long address becomes the host plus
// a shortened path, so that one address cannot fill a line.
func shortenLink(link string) string {
	if len(link) <= maxLinkText {
		return link
	}
	bare := strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://")
	if len(bare) <= maxLinkText {
		return bare
	}
	return bare[:maxLinkText-1] + "\u2026"
}

// appendText adds a text segment, joining it to the previous one when the
// previous segment is also text.
func appendText(list []Segment, text string) []Segment {
	if text == "" {
		return list
	}
	if len(list) > 0 && !list[len(list)-1].IsLink {
		list[len(list)-1].Text += text
		return list
	}
	return append(list, Segment{Text: text})
}

// bodyParts converts a body into the template context form.
func bodyParts(body string) []map[string]any {
	parts := Linkify(body)
	items := make([]map[string]any, 0, len(parts))
	for _, seg := range parts {
		items = append(items, map[string]any{
			"text":    seg.Text,
			"url":     seg.URL,
			"is_link": seg.IsLink,
		})
	}
	return items
}

