package output

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/enr/terminus/internal/query"
)

// maxCell is the width beyond which a table cell is cut.
const maxCell = 40

type column struct {
	name string
	path []string
}

// table prints a list of records as a table and reports whether it did: not when verbose, not
// when the elements are not all records, and not when a record holds a list of records and the
// module declares no columns for it (the nested lists get their own tables instead).
func (w *treeWriter) table(list []any, schema, path string, depth int) bool {
	if w.verbose || len(list) == 0 {
		return false
	}
	rows := make([]map[string]any, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return false
		}
		rows[i] = m
	}
	cols, ok := w.columns(rows, schema)
	if !ok {
		return false
	}
	rows, minor := splitMinor(rows, w.tables[schema].Minor)
	cols = withValues(rows, cols)

	cells := make([][]string, len(rows))
	numeric := make([]bool, len(cols))
	widths := make([]int, len(cols))
	for j, c := range cols {
		numeric[j] = true
		widths[j] = lipgloss.Width(c.name)
	}
	for i, row := range rows {
		cells[i] = make([]string, len(cols))
		for j, c := range cols {
			v := cellValue(row, c.path)
			if _, isNumber := v.(json.Number); !isNumber && v != nil {
				numeric[j] = false
			}
			cells[i][j] = cellText(c.path[len(c.path)-1], v)
			widths[j] = max(widths[j], lipgloss.Width(cells[i][j]))
		}
	}

	// Leave out the columns that do not fit the terminal, the first one always stays.
	indent := strings.Repeat("  ", depth)
	n := len(cols)
	for w.width > 0 && n > 1 && len(indent)+sum(widths[:n])+2*(n-1) > w.width {
		n--
		w.hidden = true
	}

	line := func(values []string, style func(j int, s string) string) {
		parts := make([]string, n)
		for j := range n {
			v := values[j]
			gap := strings.Repeat(" ", widths[j]-lipgloss.Width(v))
			switch {
			case numeric[j]:
				parts[j] = gap + style(j, v)
			case j < n-1:
				parts[j] = style(j, v) + gap
			default:
				parts[j] = style(j, v)
			}
		}
		fmt.Fprintf(w.b, "%s%s\n", indent, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	header := make([]string, len(cols))
	for j, c := range cols {
		header[j] = strings.ToUpper(c.name)
	}
	line(header, func(_ int, s string) string { return w.s.dim.Render(s) })
	for _, rc := range cells {
		line(rc, func(_ int, s string) string {
			if s == "-" {
				return w.s.dim.Render(s)
			}
			return s
		})
	}

	if len(minor) > 0 {
		w.fold(minor, indent)
	}
	if _, _, named := query.ItemKey(rows[0]); w.example == "" || named && !w.namedExample {
		label, sel := itemLabel(rows[0], 0)
		w.example, w.namedExample = joinPath(path, sel), label == sel
	}
	return true
}

// withValues drops the columns empty in every row (no error, no model); the first one stays.
func withValues(rows []map[string]any, cols []column) []column {
	out := cols[:1:1]
	for _, c := range cols[1:] {
		for _, row := range rows {
			if cellText(c.path[len(c.path)-1], cellValue(row, c.path)) != "-" {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// columns returns the columns declared for the list or, lacking them, one for each field that
// fits a cell: the key field first, then by name. Records holding records are left out.
func (w *treeWriter) columns(rows []map[string]any, schema string) ([]column, bool) {
	if t, ok := w.tables[schema]; ok && len(t.Columns) > 0 {
		cols := make([]column, 0, len(t.Columns))
		for _, spec := range t.Columns {
			name, path, named := strings.Cut(spec, "=")
			if !named {
				path = spec
			}
			segments := strings.Split(path, ".")
			if !named {
				name = segments[len(segments)-1]
			}
			cols = append(cols, column{name: name, path: segments})
		}
		w.hidden = true
		return cols, true
	}

	fields := map[string]bool{}
	for _, row := range rows {
		for k, v := range row {
			if l, ok := v.([]any); ok && !isScalarList(l) {
				return nil, false
			}
			if isScalar(v) || isScalarList(v) {
				fields[k] = true
			} else {
				w.hidden = true
			}
		}
	}
	keyField, _, _ := query.ItemKey(rows[0])
	names := make([]string, 0, len(fields))
	for k := range fields {
		if k != keyField {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if fields[keyField] {
		names = append([]string{keyField}, names...)
	}
	cols := make([]column, len(names))
	for i, k := range names {
		cols[i] = column{name: k, path: []string{k}}
	}
	return cols, len(cols) > 0
}

// cellValue follows path in v; through a list it collects the value of every element.
func cellValue(v any, path []string) any {
	for i, key := range path {
		switch node := v.(type) {
		case map[string]any:
			v = node[key]
		case []any:
			var out []any
			for _, e := range node {
				switch x := cellValue(e, path[i:]).(type) {
				case nil:
				case []any:
					out = append(out, x...)
				default:
					out = append(out, x)
				}
			}
			return out
		default:
			return nil
		}
	}
	return v
}

// cellText formats a value for a table cell: as a fact value, but sizes without the exact number
// and long texts cut.
func cellText(key string, v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		s = "-"
	case string:
		s = t
		if s == "" {
			s = "-"
		}
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, cellText(key, e))
		}
		s = strings.Join(parts, ",")
		if s == "" {
			s = "-"
		}
	case map[string]any:
		s = "{…}"
	default:
		s = compactValue(key, v)
	}
	if r := []rune(s); len(r) > maxCell {
		s = string(r[:maxCell-1]) + "…"
	}
	return s
}

// splitMinor separates the minor records; when every record is minor none is.
func splitMinor(rows []map[string]any, patterns []map[string]string) (major, minor []map[string]any) {
	for _, row := range rows {
		if isMinor(row, patterns) {
			minor = append(minor, row)
		} else {
			major = append(major, row)
		}
	}
	if len(major) == 0 {
		return rows, nil
	}
	return major, minor
}

func isMinor(row map[string]any, patterns []map[string]string) bool {
	for _, p := range patterns {
		match := true
		for field, pattern := range p {
			value := ""
			switch v := row[field].(type) {
			case nil:
			case string:
				value = v
			default:
				value = scalarString(v)
			}
			if ok, _ := path.Match(pattern, value); !ok {
				match = false
				break
			}
		}
		if match && len(p) > 0 {
			return true
		}
	}
	return false
}

// fold prints the minor records as one line of names, cut to the terminal width.
func (w *treeWriter) fold(minor []map[string]any, indent string) {
	names := make([]string, len(minor))
	for i, row := range minor {
		names[i], _ = itemLabel(row, i)
	}
	head := fmt.Sprintf("+ %d more: ", len(minor))
	tail := " (-v shows them)"
	list := strings.Join(names, ", ")
	if room := w.width - len(indent) - lipgloss.Width(head+tail); w.width > 0 && lipgloss.Width(list) > room {
		r := []rune(list)
		list = string(r[:max(room-1, 0)]) + "…"
	}
	fmt.Fprintf(w.b, "%s%s\n", indent, w.s.dim.Render(head+list+tail))
}

func sum(l []int) int {
	t := 0
	for _, n := range l {
		t += n
	}
	return t
}
