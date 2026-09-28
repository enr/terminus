package module

import (
	"context"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
)

type fake struct {
	name string
	core bool
}

func (f fake) Name() string                               { return f.name }
func (f fake) Description() string                        { return "fake " + f.name }
func (f fake) Core() bool                                 { return f.core }
func (f fake) Collect(context.Context, *Env) (any, error) { return nil, nil }

type checker struct{ fake }

func (checker) Checks() []CheckInfo {
	return []CheckInfo{{ID: "x.a", Threshold: &Threshold{Warn: 1, Fail: 2}}, {ID: "x.b"}}
}
func (checker) Check(*Env, any) []model.Finding { return nil }

func registry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(fake{"system", true}, fake{"memory", true}, fake{"podman", false}, checker{fake{"x", false}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func enabled(t *testing.T, r *Registry, sel Selection) string {
	t.Helper()
	mods, err := r.Enabled(sel)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, m := range mods {
		n = append(n, m.Name())
	}
	return strings.Join(n, ",")
}

func TestResolve(t *testing.T) {
	r := registry(t)
	cases := []struct {
		sel  Selection
		want string
	}{
		{Selection{}, "memory,system"},
		{Selection{Config: map[string]bool{"podman": true, "memory": false}}, "podman,system"},
		{Selection{Config: map[string]bool{"podman": true}, Disable: []string{"podman"}}, "memory,system"},
		{Selection{Config: map[string]bool{"system": false}, Enable: []string{"system", "x"}}, "memory,system,x"},
		{Selection{Only: []string{"x"}, Enable: []string{"podman"}}, "x"},
	}
	for _, c := range cases {
		if got := enabled(t, r, c.sel); got != c.want {
			t.Errorf("%+v: %s, want %s", c.sel, got, c.want)
		}
	}

	states, _ := r.Resolve(Selection{Config: map[string]bool{"podman": false}})
	reasons := map[string]string{}
	for _, s := range states {
		reasons[s.Module.Name()] = s.Reason
	}
	if reasons["podman"] != "disabled in configuration" || reasons["system"] != "core module" || !strings.Contains(reasons["x"], "optional") {
		t.Errorf("reasons: %v", reasons)
	}

	_, err := r.Resolve(Selection{Enable: []string{"nope"}, Config: map[string]bool{"typo": true}})
	if err == nil || !strings.Contains(err.Error(), `"nope" (--modules)`) || !strings.Contains(err.Error(), `"typo" (configuration)`) {
		t.Errorf("unknown modules: %v", err)
	}
	if _, err := NewRegistry(fake{"a", true}, fake{"a", false}); err == nil {
		t.Error("duplicate module accepted")
	}
	if len(r.Checks()) != 2 {
		t.Errorf("checks: %v", r.Checks())
	}
}

func TestThreshold(t *testing.T) {
	up := Threshold{Warn: 0.8, Fail: 0.9}
	down := Threshold{Warn: 0.1, Fail: 0.05, Below: true}
	if up.Grade(0.85) != model.SeverityWarn || up.Grade(0.95) != model.SeverityFail || up.Grade(0.1) != model.SeverityOK {
		t.Error("grade up")
	}
	if down.Grade(0.07) != model.SeverityWarn || down.Grade(0.01) != model.SeverityFail || down.Grade(0.5) != model.SeverityOK {
		t.Error("grade down")
	}
	if up.Validate() != nil || down.Validate() != nil {
		t.Error("valid thresholds rejected")
	}
	if (Threshold{Warn: 2, Fail: 1}).Validate() == nil || (Threshold{Warn: 0.01, Fail: 0.1, Below: true}).Validate() == nil {
		t.Error("inverted thresholds accepted")
	}

	s := &CheckSettings{
		Disabled:   []string{"disk.*", "net.dns"},
		Thresholds: map[string]Threshold{"mem.available": {Warn: 0.2, Fail: 0.1}},
	}
	got := s.Threshold("mem.available", down)
	if got.Warn != 0.2 || !got.Below {
		t.Errorf("configured threshold must keep the direction: %+v", got)
	}
	if s.Threshold("other", up) != up {
		t.Error("default threshold not returned")
	}
	var nilSettings *CheckSettings
	var nilEnv *Env
	if nilSettings.Threshold("x", up) != up || nilEnv.Threshold("x", up) != up || nilSettings.IsDisabled("x") {
		t.Error("nil safety")
	}
	for id, want := range map[string]bool{"disk.usage": true, "net.dns": true, "net.dnsx": false, "mem.available": false} {
		if s.IsDisabled(id) != want {
			t.Errorf("IsDisabled(%s) != %v", id, want)
		}
	}
}
