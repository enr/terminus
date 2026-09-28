package module

import (
	"fmt"
	"strings"

	"github.com/enr/terminus/internal/model"
)

// CheckInfo describes a check.
type CheckInfo struct {
	ID          string
	Description string
	// Threshold is the default threshold; nil for checks without one.
	Threshold *Threshold
}

// Threshold maps a measured value to a severity.
type Threshold struct {
	Warn float64 `toml:"warn" json:"warn"`
	Fail float64 `toml:"fail" json:"fail"`
	// Below means that lower values are worse (available memory, days to expiry).
	Below bool `toml:"-" json:"below,omitempty"`
	// Unit documents the value: "ratio" (0-1), "percent", "seconds", "days", "load per cpu".
	Unit string `toml:"-" json:"unit,omitempty"`
}

// Grade returns the severity of a value.
func (t Threshold) Grade(v float64) model.Severity {
	if t.Below {
		return model.GradeBelow(v, t.Warn, t.Fail)
	}
	return model.Grade(v, t.Warn, t.Fail)
}

// Validate checks that warn comes before fail.
func (t Threshold) Validate() error {
	if t.Below && t.Warn < t.Fail {
		return fmt.Errorf("warn (%v) must be above fail (%v): lower values are worse", t.Warn, t.Fail)
	}
	if !t.Below && t.Warn > t.Fail {
		return fmt.Errorf("warn (%v) must be below fail (%v)", t.Warn, t.Fail)
	}
	return nil
}

func (t Threshold) String() string {
	op := "≥"
	if t.Below {
		op = "<"
	}
	return fmt.Sprintf("warn %s %v, fail %s %v %s", op, t.Warn, op, t.Fail, t.Unit)
}

// CheckSettings are the check options of the configuration.
type CheckSettings struct {
	// Disabled lists check IDs, or prefixes ending in ".*" (disk.*), whose findings are dropped.
	Disabled []string
	// Thresholds overrides the warn and fail values of checks, by ID.
	Thresholds map[string]Threshold
}

// Threshold returns the configured threshold of a check, keeping the direction and unit of def.
// It is safe on nil settings.
func (s *CheckSettings) Threshold(id string, def Threshold) Threshold {
	if s == nil {
		return def
	}
	t, ok := s.Thresholds[id]
	if !ok {
		return def
	}
	t.Below, t.Unit = def.Below, def.Unit
	return t
}

// IsDisabled reports whether the findings of a check are dropped. It is safe on nil settings.
func (s *CheckSettings) IsDisabled(id string) bool {
	if s == nil {
		return false
	}
	for _, d := range s.Disabled {
		if MatchCheck(d, id) {
			return true
		}
	}
	return false
}

// MatchCheck matches a check ID against a pattern: an exact ID or a prefix ending in ".*".
func MatchCheck(pattern, id string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(id, prefix)
	}
	return pattern == id
}
