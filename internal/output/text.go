package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/query"
)

// Text renders the report for a human at a terminal: summary first, then findings, module
// statuses and, optionally, the facts.
type Text struct{}

type styles struct {
	title, dim, key lipgloss.Style
	sev             map[model.Severity]lipgloss.Style
	status          map[model.ModuleStatus]lipgloss.Style
}

func newStyles(w io.Writer, color bool) styles {
	lr := lipgloss.NewRenderer(w)
	if color {
		lr.SetColorProfile(termenv.ANSI256)
	} else {
		lr.SetColorProfile(termenv.Ascii)
	}
	fg := func(c string) lipgloss.Style { return lr.NewStyle().Foreground(lipgloss.Color(c)) }
	green, yellow, red, blue, grey := fg("2"), fg("3"), fg("1"), fg("4"), fg("8")
	return styles{
		title: lr.NewStyle().Bold(true),
		dim:   grey,
		key:   blue,
		sev: map[model.Severity]lipgloss.Style{
			model.SeverityOK:   green,
			model.SeverityInfo: blue,
			model.SeverityWarn: yellow.Bold(true),
			model.SeverityFail: red.Bold(true),
		},
		status: map[model.ModuleStatus]lipgloss.Style{
			model.StatusOK:      green,
			model.StatusPartial: yellow,
			model.StatusError:   red,
			model.StatusSkipped: grey,
		},
	}
}

var severitySymbol = map[model.Severity]string{
	model.SeverityOK:   "✔",
	model.SeverityInfo: "ℹ",
	model.SeverityWarn: "⚠",
	model.SeverityFail: "✖",
}

// Render implements Renderer.
func (Text) Render(w io.Writer, r *model.Report, o Options) error {
	s := newStyles(w, o.Color)
	b := &strings.Builder{}

	host := r.Meta.Hostname
	if host == "" {
		host = "(unknown host)"
	}
	fmt.Fprintf(b, "%s %s\n", s.title.Render(host),
		s.dim.Render(fmt.Sprintf("· %s · %s", r.Meta.Timestamp.Format(time.RFC3339), humanMillis(r.Meta.DurationMs))))
	if o.Findings {
		renderSummary(b, s, r.Summary)
		renderFindings(b, s, r, o)
	}
	renderModules(b, s, r, o.Verbose)
	if o.Facts {
		renderFacts(b, s, r, o)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func renderSummary(b *strings.Builder, s styles, sum model.Summary) {
	// Zero counts are grey: the eye goes to the severities that have findings.
	count := func(sev model.Severity, n int) string {
		text := fmt.Sprintf("%s %d %s", severitySymbol[sev], n, sev)
		if n == 0 {
			return s.dim.Render(text)
		}
		return s.sev[sev].Render(text)
	}
	fmt.Fprintf(b, "%s  %s  %s  %s\n", count(model.SeverityFail, sum.Fail), count(model.SeverityWarn, sum.Warn),
		count(model.SeverityInfo, sum.Info), count(model.SeverityOK, sum.OK))
}

func renderFindings(b *strings.Builder, s styles, r *model.Report, o Options) {
	var shown []model.Finding
	for _, f := range r.Findings {
		if o.ProblemsOnly && f.Severity < model.SeverityWarn {
			continue
		}
		shown = append(shown, f)
	}
	if len(shown) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", s.title.Render("Findings"))
	idWidth := 0
	for _, f := range shown {
		idWidth = max(idWidth, len(f.ID))
	}
	for _, f := range shown {
		st := s.sev[f.Severity]
		label := st.Render(fmt.Sprintf("%s %-4s", severitySymbol[f.Severity], f.Severity))
		line := fmt.Sprintf("  %s  %s  %s", label, pad(f.ID, idWidth), f.Message)
		if f.Subject != "" {
			line += " " + s.dim.Render("["+f.Subject+"]")
		}
		b.WriteString(line + "\n")
		if !o.Verbose {
			continue
		}
		indent := strings.Repeat(" ", 10)
		if f.Hint != "" {
			fmt.Fprintf(b, "%s%s %s\n", indent, s.key.Render("hint:"), f.Hint)
		}
		for _, k := range sortedKeys(f.Evidence) {
			fmt.Fprintf(b, "%s%s %s\n", indent, s.key.Render(k+":"), humanValue(k, f.Evidence[k]))
		}
	}
}

// moduleStatuses is the order of the counts in the Modules heading.
var moduleStatuses = []model.ModuleStatus{model.StatusOK, model.StatusPartial, model.StatusError, model.StatusSkipped}

// renderModules prints the modules that did not collect their facts normally, all of them when
// verbose, under a heading counting them by status.
func renderModules(b *strings.Builder, s styles, r *model.Report, verbose bool) {
	all := r.ModuleNames()
	if len(all) == 0 {
		return
	}
	counts := map[model.ModuleStatus]int{}
	var names []string
	for _, n := range all {
		st := r.Modules[n].Status
		counts[st]++
		if verbose || st != model.StatusOK {
			names = append(names, n)
		}
	}
	var parts []string
	for _, st := range moduleStatuses {
		if counts[st] > 0 {
			parts = append(parts, s.status[st].Render(fmt.Sprintf("%d %s", counts[st], st)))
		}
	}
	fmt.Fprintf(b, "\n%s  %s\n", s.title.Render("Modules"), strings.Join(parts, s.dim.Render(" · ")))
	nameWidth, statusWidth := 0, 0
	for _, n := range names {
		nameWidth = max(nameWidth, len(n))
		statusWidth = max(statusWidth, len(r.Modules[n].Status))
	}
	for _, n := range names {
		m := r.Modules[n]
		status := s.status[m.Status].Render(pad(string(m.Status), statusWidth))
		detail := s.dim.Render(humanMillis(m.DurationMs))
		if m.Status == model.StatusSkipped {
			detail = s.dim.Render(m.SkipReason)
		}
		fmt.Fprintf(b, "  %s  %s  %s\n", pad(n, nameWidth), status, detail)
		for _, e := range m.Errors {
			fmt.Fprintf(b, "  %s  %s\n", strings.Repeat(" ", nameWidth), s.status[model.StatusError].Render("↳ "+e))
		}
	}
}

func renderFacts(b *strings.Builder, s styles, r *model.Report, o Options) {
	tree, err := query.Generic(r.FactsTree())
	if err != nil {
		fmt.Fprintf(b, "\nfacts not available: %v\n", err)
		return
	}
	root, _ := tree.(map[string]any)
	if len(root) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", s.title.Render("Facts"))
	w := newTreeWriter(b, s, o)
	w.write(root, "", "", 1)
	w.footer()
}

// RenderFactsValue renders the value found at path (schemaPath as query.ResolveSchema returns
// it) as the facts section of the text output does.
func RenderFactsValue(out io.Writer, path, schemaPath string, v any, o Options) error {
	b := &strings.Builder{}
	w := newTreeWriter(b, newStyles(out, o.Color), o)
	w.write(v, schemaPath, path, 0)
	w.footer()
	_, err := io.WriteString(out, b.String())
	return err
}

// Beyond these sizes lists of values and maps of values are summarized unless verbose.
const (
	maxInlineList = 12
	maxScalarMap  = 25
)

type treeWriter struct {
	b       *strings.Builder
	s       styles
	verbose bool
	width   int
	tables  map[string]module.Table
	// hidden tells that a table left fields out; example is the path of a table row, shown in
	// the footer to tell how to see all its fields.
	hidden       bool
	example      string
	namedExample bool
}

func newTreeWriter(b *strings.Builder, s styles, o Options) *treeWriter {
	return &treeWriter{b: b, s: s, verbose: o.Verbose, width: o.Width, tables: o.Tables}
}

// write prints a generic tree as indented "key: value" lines; lists of scalars stay on one line,
// lists of records become tables unless verbose. schema is the path of v as map keys only (the
// key of the table columns), path the one that selects it (for the footer example).
func (w *treeWriter) write(v any, schema, path string, depth int) {
	b, s := w.b, w.s
	indent := strings.Repeat("  ", depth)
	switch node := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(node) {
			child := node[k]
			if isScalar(child) && !isEmptyMap(child) {
				fmt.Fprintf(b, "%s%s %s\n", indent, s.key.Render(k+":"), w.scalar(k, child))
				continue
			}
			if !w.verbose && allZero(child) {
				fmt.Fprintf(b, "%s%s %s\n", indent, s.key.Render(k+":"), s.dim.Render("all 0"))
				continue
			}
			if summary, ok := w.summarize(child); ok {
				fmt.Fprintf(b, "%s%s %s\n", indent, s.key.Render(k+":"), s.dim.Render(summary))
				continue
			}
			if isScalar(child) || isScalarList(child) {
				fmt.Fprintf(b, "%s%s %s\n", indent, s.key.Render(k+":"), scalarString(child))
				continue
			}
			fmt.Fprintf(b, "%s%s\n", indent, s.key.Render(k+":"))
			w.write(child, joinPath(schema, k), joinPath(path, k), depth+1)
		}
	case []any:
		if w.table(node, schema, path, depth) {
			return
		}
		for i, child := range node {
			if isScalar(child) {
				fmt.Fprintf(b, "%s- %s\n", indent, scalarString(child))
				continue
			}
			label, selector := itemLabel(child, i)
			fmt.Fprintf(b, "%s%s\n", indent, s.title.Render(label))
			w.write(child, schema, joinPath(path, selector), depth+1)
		}
	default:
		fmt.Fprintf(b, "%s%s\n", indent, scalarString(v))
	}
}

// itemLabel names a list element by its key field (eth0, /srv) or, lacking one, by its index;
// selector is what selects it in a path.
func itemLabel(v any, i int) (label, selector string) {
	index := strconv.Itoa(i)
	if m, ok := v.(map[string]any); ok {
		if _, name, ok := query.ItemKey(m); ok {
			if strings.Contains(name, ".") {
				return name, index
			}
			return name, name
		}
	}
	return "[" + index + "]", index
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// footer tells how to see what the tables leave out.
func (w *treeWriter) footer() {
	if !w.hidden {
		return
	}
	note := "tables show the main fields: -v shows them all"
	if w.example != "" {
		note += ", terminus facts " + w.example + " one record"
	}
	fmt.Fprintf(w.b, "\n%s\n", w.s.dim.Render(note))
}

// isScalar reports whether v fits on one line; empty maps and lists do.
func isScalar(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		return len(t) == 0
	case []any:
		return false
	}
	return true
}

// summarize replaces long lists of values and big maps of values (raw tables such as meminfo)
// with their size, unless verbose.
func (w treeWriter) summarize(v any) (string, bool) {
	if w.verbose {
		return "", false
	}
	switch t := v.(type) {
	case []any:
		if len(t) > maxInlineList && isScalarList(t) {
			return fmt.Sprintf("(%d items, -v to show)", len(t)), true
		}
	case map[string]any:
		if len(t) > maxScalarMap {
			for _, c := range t {
				if !isScalar(c) {
					return "", false
				}
			}
			return fmt.Sprintf("(%d entries, -v to show)", len(t)), true
		}
	}
	return "", false
}

func isEmptyMap(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 0
}

// scalar formats a fact value, humanizing sizes, ratios and durations by key; the exact number
// of bytes only when verbose.
func (w *treeWriter) scalar(key string, v any) string {
	if _, isString := v.(string); isString || v == nil {
		return scalarString(v)
	}
	if w.verbose {
		return humanValue(key, v)
	}
	return compactValue(key, v)
}

// allZero tells whether v is a map of maps whose values, at least two, are all the number 0:
// counters that never moved (pressure stall totals, error counters).
func allZero(v any) bool {
	n := 0
	var walk func(any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			for _, c := range t {
				if !walk(c) {
					return false
				}
			}
			return true
		case json.Number:
			n++
			f, err := t.Float64()
			return err == nil && f == 0
		}
		return false
	}
	_, isMap := v.(map[string]any)
	return isMap && walk(v) && n >= 2
}

func isScalarList(v any) bool {
	l, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range l {
		if !isScalar(e) {
			return false
		}
	}
	return true
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case map[string]any:
		return "{}"
	case string:
		if t == "" {
			return `""`
		}
		return t
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = scalarString(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprint(t)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// humanValue formats values by key suffix: _bytes in binary units, _ratio as a percentage,
// _seconds, _ms and _us as a duration.
func humanValue(key string, v any) string {
	switch {
	case strings.HasSuffix(key, "_ms"):
		if f, ok := toFloat(v); ok {
			return HumanDuration(time.Duration(f * float64(time.Millisecond)))
		}
	case strings.HasSuffix(key, "_us"):
		if f, ok := toFloat(v); ok {
			return HumanDuration(time.Duration(f * float64(time.Microsecond)))
		}
	case strings.HasSuffix(key, "_bytes"):
		if n, ok := toUint(v); ok {
			return fmt.Sprintf("%s (%d)", HumanBytes(n), n)
		}
	case strings.HasSuffix(key, "_ratio"):
		if f, ok := toFloat(v); ok {
			return fmt.Sprintf("%.1f%%", f*100)
		}
	case strings.HasSuffix(key, "_seconds"):
		if f, ok := toFloat(v); ok {
			return HumanDuration(time.Duration(f * float64(time.Second)))
		}
	}
	if n, ok := v.(json.Number); ok {
		v = numberValue(n)
	}
	if f, ok := v.(float64); ok {
		return formatFloat(f)
	}
	return fmt.Sprint(v)
}

// compactValue is humanValue with sizes in binary units only: 3.8 GiB.
func compactValue(key string, v any) string {
	if strings.HasSuffix(key, "_bytes") {
		if n, ok := toUint(v); ok {
			return HumanBytes(n)
		}
	}
	return humanValue(key, v)
}

// formatFloat prints at most three decimals and no exponent: 2800, 16.83, 0.043.
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 3, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

func numberValue(n json.Number) any {
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	if u, ok := toUint(v); ok {
		return float64(u), true
	}
	return 0, false
}

func toUint(v any) (uint64, bool) {
	if n, ok := v.(json.Number); ok {
		v = numberValue(n)
	}
	switch n := v.(type) {
	case uint64:
		return n, true
	case int64:
		return uint64(n), n >= 0
	case int:
		return uint64(n), n >= 0
	case float64:
		return uint64(n), n >= 0
	}
	return 0, false
}

// HumanBytes formats a size with binary units: 3.8 GiB.
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanMillis(ms int64) string {
	return HumanDuration(time.Duration(ms) * time.Millisecond)
}

// HumanDuration formats a duration compactly: 850ms, 12.3s, 5m10s, 2d4h.
func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
