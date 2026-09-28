// Package config loads terminus.toml.
//
//	timeout = "30s"                     # per module
//	modules_dir = "/etc/terminus/modules.d"
//
//	[modules.podman]
//	enabled = true                      # plus the module's own keys
//
//	[checks]
//	disable = ["net.public-listeners", "disk.*"]
//
//	[checks.thresholds."disk.usage"]
//	warn = 0.80
//	fail = 0.90
//
//	[checks.exclude]
//	"unit.memory-limit" = ["*:app-*.service"]
//
// Decoding is strict: unknown keys, modules and checks are errors, so that a typo cannot
// silently disable something.
package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/enr/terminus/internal/module"
)

// DefaultPath is where terminus looks for its configuration.
const DefaultPath = "/etc/terminus/terminus.toml"

// LocalPath is the per-run layer, read from the current directory.
const LocalPath = "terminus.toml"

// DefaultModulesDir holds the external modules.
const DefaultModulesDir = "/etc/terminus/modules.d"

type file struct {
	Timeout    string                    `toml:"timeout"`
	ModulesDir string                    `toml:"modules_dir"`
	Modules    map[string]toml.Primitive `toml:"modules"`
	Checks     struct {
		Disable    []string                    `toml:"disable"`
		Thresholds map[string]module.Threshold `toml:"thresholds"`
		Exclude    map[string][]string         `toml:"exclude"`
	} `toml:"checks"`
}

// Config is a loaded configuration. The zero value (no file) means defaults everywhere.
type Config struct {
	// Path of the loaded file; empty when none was found.
	Path string
	// Timeout per module; zero when not set.
	Timeout time.Duration
	// ModulesDir is the external modules directory; empty when not set.
	ModulesDir string
	// Enabled holds the "enabled" key of the [modules.<name>] sections that set it.
	Enabled map[string]bool
	// Checks are the check settings.
	Checks module.CheckSettings

	md       toml.MetaData
	sections map[string]toml.Primitive
}

// Load reads the configuration at path. A missing file is an error only when required is true
// (the path was given explicitly); otherwise it yields the defaults.
func Load(path string, required bool) (*Config, error) {
	c := &Config{Enabled: map[string]bool{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := c.parse(string(data)); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	return c, nil
}

// UserPath is the per-user layer: $XDG_CONFIG_HOME/terminus/terminus.toml, or
// ~/.config/terminus/terminus.toml when XDG_CONFIG_HOME is unset. Empty when it cannot be
// determined (no home directory).
func UserPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "terminus", "terminus.toml")
}

// LoadHierarchy merges the system, user and per-run layers: DefaultPath, UserPath and
// LocalPath, in that order. Each layer overrides the keys it sets; tables merge key by key so
// that, say, a user file can disable one check without repeating the system file's modules.
// Layers that don't exist are skipped, and the zero value applies when none exist. Explicit
// --config bypasses this and loads a single file instead (see Load).
func LoadHierarchy() (*Config, error) {
	return loadHierarchy(HierarchyPaths())
}

// HierarchyPaths returns the layers LoadHierarchy considers, weakest first.
func HierarchyPaths() []string {
	paths := []string{DefaultPath}
	if p := UserPath(); p != "" {
		paths = append(paths, p)
	}
	return append(paths, LocalPath)
}

func loadHierarchy(paths []string) (*Config, error) {
	m, err := Merge(paths)
	if err != nil {
		return nil, err
	}
	c := &Config{Enabled: map[string]bool{}}
	loaded := m.Loaded()
	if len(loaded) == 0 {
		return c, nil
	}
	src, err := m.TOML()
	if err != nil {
		return nil, err
	}
	if err := c.parse(src); err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(loaded, ", "), err)
	}
	c.Path = strings.Join(loaded, ", ")
	return c, nil
}

// Layer is a file considered while merging the configuration.
type Layer struct {
	Path  string
	Found bool
}

// Merged is the result of merging configuration layers, before any validation.
type Merged struct {
	// Layers are the files considered, weakest first.
	Layers []Layer
	// Table is the merged configuration; empty when no layer was found.
	Table map[string]any
}

// Merge reads the layers at paths, weakest first, and merges them (see LoadHierarchy). Missing
// files are skipped; unreadable files and TOML syntax errors are errors. The result is not
// validated: it is what terminus would decode.
func Merge(paths []string) (*Merged, error) {
	m := &Merged{Table: map[string]any{}}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			m.Layers = append(m.Layers, Layer{Path: p})
			continue
		}
		if err != nil {
			return nil, err
		}
		var t map[string]any
		if _, err := toml.Decode(string(data), &t); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		m.Table = mergeTables(m.Table, t)
		m.Layers = append(m.Layers, Layer{Path: p, Found: true})
	}
	return m, nil
}

// Loaded returns the paths of the layers that were found.
func (m *Merged) Loaded() []string {
	var paths []string
	for _, l := range m.Layers {
		if l.Found {
			paths = append(paths, l.Path)
		}
	}
	return paths
}

// TOML encodes the merged configuration, keys sorted, a blank line before each table.
func (m *Merged) TOML() (string, error) {
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(m.Table); err != nil {
		return "", fmt.Errorf("merging configuration: %w", err)
	}
	// The encoder writes no multi-line strings, so a line starting with "[" is a table header.
	// Headers of tables holding only subtables ("[modules]" before "[modules.http]") are noise.
	// The encoder's own blank lines are inconsistent: they are dropped, and one is added before
	// each table instead.
	var lines []string
	for _, l := range strings.SplitAfter(buf.String(), "\n") {
		if l != "\n" && l != "" {
			lines = append(lines, l)
		}
	}
	isHeader := func(i int) bool { return i < len(lines) && strings.HasPrefix(lines[i], "[") }
	var out strings.Builder
	for i, l := range lines {
		// An empty leaf table ("[modules.typo]") is kept: it still matters to validation.
		if isHeader(i) && isHeader(i+1) &&
			strings.HasPrefix(lines[i+1], strings.TrimSuffix(strings.TrimSpace(l), "]")+".") {
			continue
		}
		if isHeader(i) && out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(l)
	}
	return out.String(), nil
}

// mergeTables merges src into dst, recursing into nested tables; any other value (including
// arrays, so "checks.disable" is replaced wholesale rather than concatenated) is overwritten by
// src.
func mergeTables(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				dst[k] = mergeTables(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

// Parse reads a configuration from a string (tests, --config -).
func Parse(data string) (*Config, error) {
	c := &Config{Enabled: map[string]bool{}}
	if err := c.parse(data); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) parse(data string) error {
	var f file
	md, err := toml.Decode(data, &f)
	if err != nil {
		return err
	}
	c.md, c.sections = md, f.Modules
	if f.Timeout != "" {
		if c.Timeout, err = ParseDuration(f.Timeout); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
	}
	c.ModulesDir = f.ModulesDir
	c.Checks = module.CheckSettings{Disabled: f.Checks.Disable, Thresholds: f.Checks.Thresholds, Exclude: f.Checks.Exclude}
	for name, prim := range f.Modules {
		var s struct {
			Enabled *bool `toml:"enabled"`
		}
		if err := md.PrimitiveDecode(prim, &s); err != nil {
			return fmt.Errorf("modules.%s: %w", name, err)
		}
		if s.Enabled != nil {
			c.Enabled[name] = *s.Enabled
		}
	}
	return nil
}

// Decoder returns the decoder of the [modules.<name>] section; without a section it leaves the
// value untouched.
func (c *Config) Decoder(name string) module.Decoder {
	return func(v any) error {
		prim, ok := c.sections[name]
		if !ok {
			return nil
		}
		if err := c.md.PrimitiveDecode(prim, v); err != nil {
			return fmt.Errorf("modules.%s: %w", name, err)
		}
		return nil
	}
}

// Configure passes to every configurable module its section. It must run before Validate, which
// reports the keys no module decoded.
func (c *Config) Configure(reg *module.Registry) error {
	var errs []error
	for _, m := range reg.All() {
		if cm, ok := m.(module.Configurable); ok {
			if err := cm.Configure(c.Decoder(m.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Validate reports what the configuration names but terminus does not know: modules, keys,
// checks, thresholds of checks that have none, and inconsistent thresholds.
func (c *Config) Validate(reg *module.Registry) error {
	var errs []error
	var names []string
	for name := range c.sections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := reg.Get(name); !ok {
			errs = append(errs, fmt.Errorf("unknown module %q in [modules.%s]", name, name))
		}
	}
	for _, k := range c.md.Undecoded() {
		// Sections of unknown modules are already reported.
		if len(k) >= 2 && k[0] == "modules" {
			if _, ok := reg.Get(k[1]); !ok {
				continue
			}
		}
		errs = append(errs, fmt.Errorf("unknown key %q", k.String()))
	}

	checks := reg.Checks()
	for _, pattern := range c.Checks.Disabled {
		if !matchesAny(pattern, checks, reg) {
			errs = append(errs, fmt.Errorf("checks.disable: no check matches %q", pattern))
		}
	}
	var excludePatterns []string
	for pattern := range c.Checks.Exclude {
		excludePatterns = append(excludePatterns, pattern)
	}
	sort.Strings(excludePatterns)
	for _, pattern := range excludePatterns {
		if !matchesAny(pattern, checks, reg) {
			errs = append(errs, fmt.Errorf("checks.exclude: no check matches %q", pattern))
		}
		for _, g := range c.Checks.Exclude[pattern] {
			if _, err := path.Match(g, ""); err != nil {
				errs = append(errs, fmt.Errorf("checks.exclude.%q: invalid pattern %q: %w", pattern, g, err))
			}
		}
	}

	var ids []string
	for id := range c.Checks.Thresholds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		info, ok := checks[id]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("checks.thresholds: unknown check %q", id))
		case info.Threshold == nil:
			errs = append(errs, fmt.Errorf("checks.thresholds: check %q has no threshold", id))
		default:
			if !c.md.IsDefined("checks", "thresholds", id, "warn") || !c.md.IsDefined("checks", "thresholds", id, "fail") {
				errs = append(errs, fmt.Errorf("checks.thresholds.%q: both warn and fail are required", id))
				continue
			}
			t := c.Checks.Threshold(id, *info.Threshold)
			if err := t.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("checks.thresholds.%q: %w", id, err))
			}
		}
	}
	return errors.Join(errs...)
}

// matchesAny tells whether a disable pattern matches a known check. External modules cannot
// declare their checks in advance: patterns starting with their name are accepted.
func matchesAny(pattern string, checks map[string]module.CheckInfo, reg *module.Registry) bool {
	for id := range checks {
		if module.MatchCheck(pattern, id) {
			return true
		}
	}
	prefix, _, _ := strings.Cut(pattern, ".")
	if m, ok := reg.Get(prefix); ok {
		if ext, ok := m.(module.External); ok && ext.External() {
			return true
		}
	}
	return false
}

// ParseDuration extends time.ParseDuration with days: "14d", "1d12h".
func ParseDuration(s string) (time.Duration, error) {
	if days, rest, ok := strings.Cut(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d := time.Duration(n) * 24 * time.Hour
		if rest == "" {
			return d, nil
		}
		r, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return d + r, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}
