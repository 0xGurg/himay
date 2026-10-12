// Package textutil provides display-width-aware string helpers.
package textutil

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Width is the display width of s, ignoring ANSI sequences and counting
// grapheme clusters (so combining marks such as Baybayin vowel signs and
// virama do not add columns).
func Width(s string) int { return ansi.StringWidth(s) }

// Truncate shortens s to at most w columns, appending "…" when cut.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// Pad truncates or right-pads s to exactly w columns.
func Pad(s string, w int) string {
	s = Truncate(s, w)
	if d := w - Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

// Wrap hard-wraps plain text to w columns, preserving existing newlines.
func Wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = Sanitize(line)
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(ansi.Wrap(line, w, " "), "\n")...)
	}
	return out
}

// Sanitize strips ANSI sequences and control characters from untrusted
// content (tool output may contain escape codes that would corrupt the UI).
func Sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// OneLine collapses whitespace so s fits on a single row.
func OneLine(s string) string { return strings.Join(strings.Fields(Sanitize(s)), " ") }
