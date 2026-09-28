// Package diff compares two reports: what got worse or better between a "before" and an "after"
// snapshot (a deploy, an incident, a configuration change).
package diff

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/query"
)

// Kinds of change.
const (
	New      = "new"
	Resolved = "resolved"
	Changed  = "changed"
	Added    = "added"
	Removed  = "removed"
)

// FindingChange is a finding that appeared, disappeared or changed severity.
type FindingChange struct {
	Kind    string         `json:"kind"`
	ID      string         `json:"id"`
	Module  string         `json:"module"`
	Subject string         `json:"subject,omitempty"`
	Before  *model.Finding `json:"before,omitempty"`
	After   *model.Finding `json:"after,omitempty"`
}

// Worse tells whether the change is a regression: a new problem or a higher severity.
func (c FindingChange) Worse() bool {
	switch c.Kind {
	case New:
		return c.After.Severity >= model.SeverityWarn
	case Changed:
		return c.After.Severity > c.Before.Severity && c.After.Severity >= model.SeverityWarn
	}
	return false
}

// FactChange is a fact that appeared, disappeared or changed value.
type FactChange struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// Snapshot identifies a report.
type Snapshot struct {
	Hostname  string `json:"hostname"`
	Timestamp string `json:"timestamp"`
}

// Result is the comparison of two reports.
type Result struct {
	Before   Snapshot        `json:"before"`
	After    Snapshot        `json:"after"`
	Findings []FindingChange `json:"findings"`
	Facts    []FactChange    `json:"facts,omitempty"`
}

// ExitCode is 2 when a failure appeared, 1 when a warning appeared, 0 otherwise.
func (r Result) ExitCode() int {
	code := model.ExitOK
	for _, c := range r.Findings {
		if !c.Worse() {
			continue
		}
		if c.After.Severity == model.SeverityFail {
			return model.ExitFail
		}
		code = model.ExitWarn
	}
	return code
}

// DefaultIgnore are the facts that change at every run: times, counters, current usage.
var DefaultIgnore = []string{
	"*.time.*", "*uptime*", "*boot_time*", "*.load.*", "*pressure*", "*.stats.*", "*meminfo*",
	"*_current*", "*current_bytes*", "*_ratio", "*available_bytes", "*free_bytes", "*used_bytes",
	"*cached_bytes", "*buffers_bytes", "*dirty_bytes", "*inodes_used*", "*cpu_usage*",
	"*.history.*", "*duration*", "*started_at", "*finished_at", "*active_since", "*.pid",
	"*lines*", "*disk_usage_bytes", "*size_bytes*", "*.files", "*memory_peak_bytes", "*tasks_current",
	"*.health_failing_streak", "*.health_last_output", "*.mhz",
}

// Load reads a JSON report.
func Load(path string) (*model.Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r model.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: not a terminus JSON report: %w", path, err)
	}
	if r.SchemaVersion == 0 {
		return nil, fmt.Errorf("%s: not a terminus JSON report (no schema_version)", path)
	}
	if r.SchemaVersion > model.SchemaVersion {
		return nil, fmt.Errorf("%s: schema version %d is newer than this terminus (%d)", path, r.SchemaVersion, model.SchemaVersion)
	}
	return &r, nil
}

// Compare compares the findings of two reports and, when facts is true, their facts, leaving out
// the paths matching the ignore patterns ("*" matches any text, dots included).
func Compare(before, after *model.Report, facts bool, ignore []string) Result {
	res := Result{
		Before:   Snapshot{before.Meta.Hostname, before.Meta.Timestamp.Format("2006-01-02T15:04:05Z07:00")},
		After:    Snapshot{after.Meta.Hostname, after.Meta.Timestamp.Format("2006-01-02T15:04:05Z07:00")},
		Findings: compareFindings(before.Findings, after.Findings),
	}
	if facts {
		res.Facts = compareFacts(flatten(before), flatten(after), ignore)
	}
	return res
}

func key(f model.Finding) string { return f.ID + "\x00" + f.Subject }

func compareFindings(before, after []model.Finding) []FindingChange {
	b := map[string]model.Finding{}
	for _, f := range before {
		b[key(f)] = f
	}
	out := []FindingChange{}
	seen := map[string]bool{}
	for _, f := range after {
		k := key(f)
		seen[k] = true
		old, ok := b[k]
		switch {
		case !ok:
			if f.Severity > model.SeverityOK {
				out = append(out, FindingChange{Kind: New, ID: f.ID, Module: f.Module, Subject: f.Subject, After: ptr(f)})
			}
		case old.Severity != f.Severity:
			out = append(out, FindingChange{Kind: Changed, ID: f.ID, Module: f.Module, Subject: f.Subject, Before: ptr(old), After: ptr(f)})
		}
	}
	for _, f := range before {
		if !seen[key(f)] && f.Severity > model.SeverityOK {
			out = append(out, FindingChange{Kind: Resolved, ID: f.ID, Module: f.Module, Subject: f.Subject, Before: ptr(f)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := out[i].Worse(), out[j].Worse()
		if wi != wj {
			return wi
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

func ptr(f model.Finding) *model.Finding { return &f }

// flatten turns the facts into path → scalar. List elements with a name (name, mount_point, id)
// are keyed by it, so that an element added in the middle does not shift the others.
func flatten(r *model.Report) map[string]any {
	out := map[string]any{}
	tree, err := query.Generic(r.FactsTree())
	if err != nil {
		return out
	}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, c := range t {
				walk(join(prefix, k), c)
			}
		case []any:
			if len(t) == 0 {
				return
			}
			scalars := true
			for _, c := range t {
				if _, ok := c.(map[string]any); ok {
					scalars = false
				}
			}
			if scalars {
				out[prefix] = fmt.Sprint(t)
				return
			}
			for i, c := range t {
				walk(join(prefix, elementKey(c, i)), c)
			}
		default:
			out[prefix] = t
		}
	}
	walk("", tree)
	return out
}

func elementKey(v any, i int) string {
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"name", "mount_point", "id"} {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return strconv.Itoa(i)
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

func compareFacts(before, after map[string]any, ignore []string) []FactChange {
	skip := func(p string) bool {
		for _, g := range ignore {
			if Match(g, p) {
				return true
			}
		}
		return false
	}
	var out []FactChange
	for p, a := range after {
		if skip(p) {
			continue
		}
		b, ok := before[p]
		switch {
		case !ok:
			out = append(out, FactChange{Kind: Added, Path: p, After: a})
		case fmt.Sprint(a) != fmt.Sprint(b):
			out = append(out, FactChange{Kind: Changed, Path: p, Before: b, After: a})
		}
	}
	for p, b := range before {
		if _, ok := after[p]; !ok && !skip(p) {
			out = append(out, FactChange{Kind: Removed, Path: p, Before: b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Match is a glob where "*" matches any text, dots included.
func Match(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		last := i == len(parts)-2
		if last {
			return strings.HasSuffix(s, p)
		}
		idx := strings.Index(s, p)
		if idx < 0 {
			return false
		}
		s = s[idx+len(p):]
	}
	return true
}
