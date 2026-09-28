// Package model holds the data types shared by modules, the engine and the renderers.
package model

import (
	"fmt"
	"sort"
	"time"
)

// SchemaVersion is the version of the JSON report schema. Bump it on breaking changes.
const SchemaVersion = 1

// Exit codes returned by the CLI.
const (
	ExitOK    = 0
	ExitWarn  = 1
	ExitFail  = 2
	ExitError = 3
)

// Severity of a finding.
type Severity int

// Severities, from the least to the most severe.
const (
	SeverityOK Severity = iota
	SeverityInfo
	SeverityWarn
	SeverityFail
)

var severityNames = []string{"ok", "info", "warn", "fail"}

func (s Severity) String() string {
	if s < 0 || int(s) >= len(severityNames) {
		return fmt.Sprintf("severity(%d)", int(s))
	}
	return severityNames[s]
}

// MarshalText implements encoding.TextMarshaler.
func (s Severity) MarshalText() ([]byte, error) {
	if s < 0 || int(s) >= len(severityNames) {
		return nil, fmt.Errorf("invalid severity %d", int(s))
	}
	return []byte(severityNames[s]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Severity) UnmarshalText(b []byte) error {
	for i, n := range severityNames {
		if n == string(b) {
			*s = Severity(i)
			return nil
		}
	}
	return fmt.Errorf("invalid severity %q", string(b))
}

// Grade maps a value to a severity: fail at or above failAt, warn at or above warnAt, ok below.
func Grade(v, warnAt, failAt float64) Severity {
	switch {
	case v >= failAt:
		return SeverityFail
	case v >= warnAt:
		return SeverityWarn
	default:
		return SeverityOK
	}
}

// GradeBelow is Grade for values where lower is worse: fail below failBelow, warn below warnBelow.
func GradeBelow(v, warnBelow, failBelow float64) Severity {
	switch {
	case v < failBelow:
		return SeverityFail
	case v < warnBelow:
		return SeverityWarn
	default:
		return SeverityOK
	}
}

// Finding is the result of a check.
type Finding struct {
	ID       string         `json:"id"`
	Module   string         `json:"module"`
	Severity Severity       `json:"severity"`
	Subject  string         `json:"subject,omitempty"`
	Message  string         `json:"message"`
	Evidence map[string]any `json:"evidence,omitempty"`
	Hint     string         `json:"hint,omitempty"`
}

// ModuleStatus is the outcome of a module collection.
type ModuleStatus string

// Module statuses.
const (
	StatusOK      ModuleStatus = "ok"
	StatusPartial ModuleStatus = "partial"
	StatusError   ModuleStatus = "error"
	StatusSkipped ModuleStatus = "skipped"
)

// ModuleResult holds the facts collected by a module and what went wrong while collecting them.
type ModuleResult struct {
	Name       string       `json:"name"`
	Status     ModuleStatus `json:"status"`
	Facts      any          `json:"facts,omitempty"`
	Errors     []string     `json:"errors,omitempty"`
	SkipReason string       `json:"skip_reason,omitempty"`
	DurationMs int64        `json:"duration_ms"`
}

// Meta describes the run that produced a report.
type Meta struct {
	Tool       string    `json:"tool"`
	Version    string    `json:"version,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	Hostname   string    `json:"hostname"`
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"duration_ms"`
}

// Summary counts findings by severity.
type Summary struct {
	OK   int `json:"ok"`
	Info int `json:"info"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// Report is the complete output of a run.
type Report struct {
	SchemaVersion int                     `json:"schema_version"`
	Meta          Meta                    `json:"meta"`
	Modules       map[string]ModuleResult `json:"modules"`
	Findings      []Finding               `json:"findings"`
	Summary       Summary                 `json:"summary"`
}

// NewReport returns an empty report.
func NewReport() *Report {
	return &Report{
		SchemaVersion: SchemaVersion,
		Modules:       map[string]ModuleResult{},
		Findings:      []Finding{},
	}
}

// AddFindings appends findings and keeps them sorted (most severe first, then by ID and subject)
// and the summary up to date.
func (r *Report) AddFindings(fs ...Finding) {
	r.Findings = append(r.Findings, fs...)
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Subject < b.Subject
	})
	r.Summary = Summary{}
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityOK:
			r.Summary.OK++
		case SeverityInfo:
			r.Summary.Info++
		case SeverityWarn:
			r.Summary.Warn++
		case SeverityFail:
			r.Summary.Fail++
		}
	}
}

// ModuleNames returns the names of the modules in the report, sorted.
func (r *Report) ModuleNames() []string {
	names := make([]string, 0, len(r.Modules))
	for n := range r.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// FactsTree returns the facts of every module keyed by module name.
func (r *Report) FactsTree() map[string]any {
	t := make(map[string]any, len(r.Modules))
	for n, m := range r.Modules {
		if m.Facts != nil {
			t[n] = m.Facts
		}
	}
	return t
}

// ExitCode maps the report to the CLI exit code: fail findings win over warn ones.
func (r *Report) ExitCode() int {
	switch {
	case r.Summary.Fail > 0:
		return ExitFail
	case r.Summary.Warn > 0:
		return ExitWarn
	default:
		return ExitOK
	}
}
