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
// Decoding is strict: unknown keys, modules and checks are errors, so that a typo cannot
// silently disable something.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/enr/terminus/internal/module"
)

// DefaultPath is where terminus looks for its configuration.
const DefaultPath = "/etc/terminus/terminus.toml"

// DefaultModulesDir holds the external modules.
const DefaultModulesDir = "/etc/terminus/modules.d"

type file struct {
	Timeout    string                    `toml:"timeout"`
	ModulesDir string                    `toml:"modules_dir"`
	Modules    map[string]toml.Primitive `toml:"modules"`
	Checks     struct {
		Disable    []string                    `toml:"disable"`
		Thresholds map[string]module.Threshold `toml:"thresholds"`
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
	c.Checks = module.CheckSettings{Disabled: f.Checks.Disable, Thresholds: f.Checks.Thresholds}
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
