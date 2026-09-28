package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

type plain struct{ name string }

func (p plain) Name() string                                      { return p.name }
func (p plain) Description() string                               { return "" }
func (p plain) Core() bool                                        { return true }
func (p plain) Collect(context.Context, *module.Env) (any, error) { return nil, nil }

type withChecks struct{ plain }

func (withChecks) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "disk.usage", Threshold: &module.Threshold{Warn: 0.85, Fail: 0.95}},
		{ID: "mem.available", Threshold: &module.Threshold{Warn: 0.1, Fail: 0.05, Below: true}},
		{ID: "disk.readonly"},
	}
}
func (withChecks) Check(*module.Env, any) []model.Finding { return nil }

type configurable struct {
	plain
	URL   string
	Count int
}

func (c *configurable) ConfigExample() string { return `url = ""` }

func (c *configurable) Configure(decode module.Decoder) error {
	var s struct {
		URL   string `toml:"url"`
		Count int    `toml:"count"`
	}
	if err := decode(&s); err != nil {
		return err
	}
	c.URL, c.Count = s.URL, s.Count
	return nil
}

type external struct{ plain }

func (external) External() bool { return true }

func registry(t *testing.T) (*module.Registry, *configurable) {
	t.Helper()
	cm := &configurable{plain: plain{"http"}}
	r, err := module.NewRegistry(withChecks{plain{"storage"}}, cm, external{plain{"backup"}}, plain{"system"})
	if err != nil {
		t.Fatal(err)
	}
	return r, cm
}

func load(t *testing.T, data string) (*Config, error) {
	t.Helper()
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	reg, _ := registry(t)
	if err := c.Configure(reg); err != nil {
		return c, err
	}
	return c, c.Validate(reg)
}

func TestValid(t *testing.T) {
	c, err := Parse(`
timeout = "45s"
modules_dir = "/opt/mods"

[modules.http]
enabled = true
url = "https://example.org"
count = 3

[modules.system]
enabled = false

[checks]
disable = ["disk.readonly", "backup.*"]

[checks.thresholds."disk.usage"]
warn = 0.7
fail = 0.8

[checks.thresholds."mem.available"]
warn = 0.2
fail = 0.1
`)
	if err != nil {
		t.Fatal(err)
	}
	reg, cm := registry(t)
	if err := c.Configure(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(reg); err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 45*time.Second || c.ModulesDir != "/opt/mods" {
		t.Errorf("general: %v %q", c.Timeout, c.ModulesDir)
	}
	if !c.Enabled["http"] || c.Enabled["system"] || len(c.Enabled) != 2 {
		t.Errorf("enabled: %v", c.Enabled)
	}
	if cm.URL != "https://example.org" || cm.Count != 3 {
		t.Errorf("module config: %+v", cm)
	}
	if th := c.Checks.Threshold("disk.usage", module.Threshold{}); th.Warn != 0.7 || th.Fail != 0.8 {
		t.Errorf("threshold: %+v", th)
	}
	if !c.Checks.IsDisabled("backup.age") || !c.Checks.IsDisabled("disk.readonly") {
		t.Errorf("disabled: %v", c.Checks.Disabled)
	}
}

func TestExclude(t *testing.T) {
	c, err := load(t, `
[checks.exclude]
"disk.readonly" = ["*:app-*.service"]
`)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := registry(t)
	if err := c.Configure(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(reg); err != nil {
		t.Fatal(err)
	}
	if !c.Checks.IsExcluded("disk.readonly", "user:enrico:app-x.service") {
		t.Error("exclude not applied")
	}
	if c.Checks.IsExcluded("disk.readonly", "system:sshd.service") {
		t.Error("exclude matched an unrelated subject")
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]string{
		`timeuot = "1s"`:                                                 `unknown key "timeuot"`,
		`timeout = "soon"`:                                               `invalid duration`,
		"[modules.htpp]\nenabled = true":                                 `unknown module "htpp"`,
		"[modules.http]\nurl = \"x\"\ncuont = 1":                         `unknown key "modules.http.cuont"`,
		"[modules.http]\ncount = \"three\"":                              `modules.http`,
		"[modules.system]\nenabled = true\nfoo = 1":                      `unknown key "modules.system.foo"`,
		"[checks]\ndisable = [\"dsk.usage\"]":                            `no check matches "dsk.usage"`,
		"[checks]\ndisable = [\"system.*\"]":                             `no check matches "system.*"`,
		"[checks.thresholds.\"disk.usge\"]\nwarn=1\nfail=2":              `unknown check "disk.usge"`,
		"[checks.thresholds.\"disk.readonly\"]\nwarn=1\nfail=2":          `has no threshold`,
		"[checks.thresholds.\"disk.usage\"]\nwarn=0.9\nfail=0.8":         `must be below fail`,
		"[checks.thresholds.\"mem.available\"]\nwarn=0.05\nfail=0.1":     `must be above fail`,
		"[checks.thresholds.\"disk.usage\"]\nwarn=0.9":                   `both warn and fail are required`,
		"[checks.thresholds.\"disk.usage\"]\nwarn=0.1\nfail=0.2\nfial=3": `unknown key`,
		"[checks.exclude]\n\"dsk.usage\" = [\"*\"]":                      `no check matches "dsk.usage"`,
		"[checks.exclude]\n\"disk.usage\" = [\"[\"]":                     `invalid pattern`,
		`not toml`: `expected`,
	}
	for data, want := range cases {
		_, err := load(t, data)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", data, err, want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "missing.toml"), false)
	if err != nil || c.Path != "" || c.Timeout != 0 {
		t.Fatalf("missing optional file: %+v %v", c, err)
	}
	if _, err := Load(filepath.Join(dir, "missing.toml"), true); err == nil {
		t.Fatal("missing required file accepted")
	}
	p := filepath.Join(dir, "terminus.toml")
	if err := os.WriteFile(p, []byte("timeout = \"1m\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p, false)
	if err != nil || c.Path != p || c.Timeout != time.Minute {
		t.Fatalf("load: %+v %v", c, err)
	}
	if err := os.WriteFile(p, []byte("timeout = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, false); err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("error without path: %v", err)
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"14d": 14 * 24 * time.Hour, "1d12h": 36 * time.Hour, "90s": 90 * time.Second}
	for in, want := range cases {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"xd", "1dx", "", "d"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
