package main

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Input limits. The platform accepts text only.
const (
	maxSubject   = 200
	maxPostBody  = 16 * 1024
	maxReplyBody = 4 * 1024
	maxChatBody  = 2 * 1024
	maxTopic     = 200
	maxEmail     = 254
	maxHandle    = 24
	minHandle    = 2
)

// CleanText removes carriage returns, removes control characters, and
// trims the outer white space. The result keeps newline characters, because
// the body of a post is multi-line text.
func CleanText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var buf strings.Builder
	buf.Grow(len(text))
	for _, chr := range text {
		if chr == '\n' || chr == '\t' {
			buf.WriteRune(chr)
			continue
		}
		if unicode.IsControl(chr) {
			continue
		}
		if chr == utf8.RuneError {
			continue
		}
		buf.WriteRune(chr)
	}
	return strings.TrimSpace(buf.String())
}

// CleanLine is CleanText for a single-line value. Newline characters
// become spaces.
func CleanLine(text string) string {
	return strings.Join(strings.Fields(CleanText(text)), " ")
}

// ValidSubject checks the subject of a post.
func ValidSubject(text string) (string, error) {
	text = CleanLine(text)
	if text == "" {
		return "", errors.New("the subject is empty")
	}
	if utf8.RuneCountInString(text) > maxSubject {
		return "", errors.New("the subject is too long")
	}
	return text, nil
}

// ValidBody checks the body of a post or of a reply.
func ValidBody(text string, limit int) (string, error) {
	text = CleanText(text)
	if text == "" {
		return "", errors.New("the message is empty")
	}
	if len(text) > limit {
		return "", errors.New("the message is too long")
	}
	return text, nil
}

// ValidTopic checks the topic line of a channel. An empty value is correct,
// because a channel needs a name only.
func ValidTopic(text string) (string, error) {
	text = CleanLine(text)
	if utf8.RuneCountInString(text) > maxTopic {
		return "", errors.New("the topic is too long")
	}
	return text, nil
}

// ValidEmail applies a minimal check. A full check is not possible, and the
// // delivery of the sign-in link is the real test of the address.
func ValidEmail(text string) (string, error) {
	text = strings.ToLower(CleanLine(text))
	if len(text) < 3 || len(text) > maxEmail {
		return "", errors.New("the email address is not valid")
	}
	pos := strings.LastIndex(text, "@")
	if pos < 1 || pos == len(text)-1 {
		return "", errors.New("the email address is not valid")
	}
	domain := text[pos+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") {
		return "", errors.New("the email address is not valid")
	}
	if strings.ContainsAny(text, " ,;<>\"\\") {
		return "", errors.New("the email address is not valid")
	}
	return text, nil
}

// ValidHandle checks the public name of a member. The set of permitted
// characters is small, because the handle appears on every page.
func ValidHandle(text string) (string, error) {
	text = CleanLine(text)
	count := utf8.RuneCountInString(text)
	if count < minHandle || count > maxHandle {
		return "", errors.New("the handle must have 2 to 24 characters")
	}
	for _, chr := range text {
		valid := (chr >= 'a' && chr <= 'z') ||
			(chr >= 'A' && chr <= 'Z') ||
			(chr >= '0' && chr <= '9') ||
			chr == '_' || chr == '-' || chr == '.'
		if !valid {
			return "", errors.New(
				"the handle accepts letters, digits, and the signs _ - .")
		}
	}
	return text, nil
}
