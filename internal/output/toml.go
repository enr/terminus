package output

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type tomlStyles struct {
	header, key, str, num, comment lipgloss.Style
}

func newTOMLStyles(w io.Writer, color bool) tomlStyles {
	lr := lipgloss.NewRenderer(w)
	if color {
		lr.SetColorProfile(termenv.ANSI256)
	} else {
		lr.SetColorProfile(termenv.Ascii)
	}
	fg := func(c string) lipgloss.Style { return lr.NewStyle().Foreground(lipgloss.Color(c)) }
	return tomlStyles{
		header:  fg("5").Bold(true),
		key:     fg("4"),
		str:     fg("2"),
		num:     fg("3"),
		comment: fg("8"),
	}
}

// HighlightTOML writes a TOML document with syntax highlighting: table headers, keys, strings,
// numbers and booleans, comments. Without color it writes src unchanged.
func HighlightTOML(w io.Writer, src string, color bool) error {
	if !color {
		_, err := io.WriteString(w, src)
		return err
	}
	s := newTOMLStyles(w, color)
	var b strings.Builder
	lines := strings.SplitAfter(src, "\n")
	for _, line := range lines {
		body := strings.TrimRight(line, "\n")
		nl := line[len(body):]
		trimmed := strings.TrimLeft(body, " \t")
		b.WriteString(body[:len(body)-len(trimmed)])
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "#"):
			b.WriteString(s.comment.Render(trimmed))
		case strings.HasPrefix(trimmed, "["):
			end := closingBracket(trimmed)
			b.WriteString(s.header.Render(trimmed[:end]))
			highlightValue(&b, s, trimmed[end:])
		default:
			if i := assignment(trimmed); i >= 0 {
				key := strings.TrimRight(trimmed[:i], " \t")
				b.WriteString(s.key.Render(key))
				b.WriteString(trimmed[len(key) : i+1])
				highlightValue(&b, s, trimmed[i+1:])
			} else {
				// Continuation of a multi-line array.
				highlightValue(&b, s, trimmed)
			}
		}
		b.WriteString(nl)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// closingBracket returns the end of a table header ("[a.\"b]\"]" or "[[a]]"), quotes aware.
func closingBracket(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\'':
			i = quoted(s, i) - 1
		case ']':
			if i+1 < len(s) && s[i+1] == ']' {
				return i + 2
			}
			return i + 1
		}
	}
	return len(s)
}

// assignment returns the index of the "=" of a key = value line, or -1.
func assignment(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\'':
			i = quoted(s, i) - 1
		case '=':
			return i
		case '#', '[', '{':
			return -1
		}
	}
	return -1
}

// quoted returns the end (exclusive) of the string starting at s[start], a quote.
func quoted(s string, start int) int {
	q := s[start]
	for i := start + 1; i < len(s); i++ {
		switch {
		case s[i] == '\\' && q == '"':
			i++
		case s[i] == q:
			return i + 1
		}
	}
	return len(s)
}

func isBare(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-' || c == '+' || c == '.' || c == ':'
}

// highlightValue writes a value (or the rest of a line after it): strings, numbers, booleans,
// dates, arrays and inline tables, whose keys get the key style.
func highlightValue(b *strings.Builder, s tomlStyles, v string) {
	for i := 0; i < len(v); {
		c := v[i]
		switch {
		case c == '"' || c == '\'':
			end := quoted(v, i)
			b.WriteString(s.str.Render(v[i:end]))
			i = end
		case c == '#':
			b.WriteString(s.comment.Render(v[i:]))
			return
		case isBare(c):
			end := i
			for end < len(v) && isBare(v[end]) {
				end++
			}
			word := v[i:end]
			if rest := strings.TrimLeft(v[end:], " "); strings.HasPrefix(rest, "=") {
				b.WriteString(s.key.Render(word)) // inline table key
			} else {
				b.WriteString(s.num.Render(word))
			}
			i = end
		default:
			b.WriteByte(c)
			i++
		}
	}
}
