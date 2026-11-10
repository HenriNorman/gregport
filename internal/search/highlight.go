package search

import (
	"html"
	"strings"
)

// ts_headline markers. Control characters are used instead of HTML tags so
// the source text can be escaped as a whole before the markers are restored;
// a ticket subject containing "<mark>" can never inject markup. They are
// uncommon enough that a normal ticket body will not contain them.
const (
	hlStart = "\x1e"
	hlEnd   = "\x1f"
)

// headlineOptions builds the ts_headline option string for a field.
func headlineOptions(maxFragments, maxWords int) string {
	return "StartSel=" + hlStart +
		",StopSel=" + hlEnd +
		",MaxFragments=" + itoa(maxFragments) +
		",MaxWords=" + itoa(maxWords) +
		",MinWords=5"
}

// RenderHighlight escapes the raw snippet and restores the matched-term
// markers as <mark> tags. The output is safe to insert into HTML: everything
// except the two <mark> tags is escaped.
func RenderHighlight(raw string) string {
	if raw == "" {
		return ""
	}
	escaped := html.EscapeString(raw)
	escaped = strings.ReplaceAll(escaped, hlStart, "<mark>")
	escaped = strings.ReplaceAll(escaped, hlEnd, "</mark>")
	return escaped
}

// itoa is a tiny helper that avoids pulling strconv into the option builder.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
