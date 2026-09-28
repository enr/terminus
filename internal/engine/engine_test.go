package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

type fakeModule struct {
	name    string
	core    bool
	collect func(ctx context.Context) (any, error)
}

func (m *fakeModule) Name() string        { return m.name }
func (m *fakeModule) Description() string { return "fake" }
func (m *fakeModule) Core() bool          { return m.core }
func (m *fakeModule) Collect(ctx context.Context, _ *module.Env) (any, error) {
	return m.collect(ctx)
}

type checkingModule struct {
	fakeModule
	check func(facts any) []model.Finding
}

func (m *checkingModule) Check(_ *module.Env, facts any) []model.Finding { return m.check(facts) }
func (m *checkingModule) Checks() []module.CheckInfo                     { return nil }

func TestRunStatuses(t *testing.T) {
	mods := []module.Module{
		&fakeModule{name: "ok", collect: func(context.Context) (any, error) { return map[string]int{"a": 1}, nil }},
		&fakeModule{name: "partial", collect: func(context.Context) (any, error) {
			return "some", errors.Join(errors.New("e1"), errors.New("e2"))
		}},
		&fakeModule{name: "error", collect: func(context.Context) (any, error) { return nil, errors.New("boom") }},
		&fakeModule{name: "panic", collect: func(context.Context) (any, error) { panic("oops") }},
		&fakeModule{name: "skipped", collect: func(context.Context) (any, error) { return nil, module.Skip("no %s", "podman") }},
		&fakeModule{name: "slow", collect: func(ctx context.Context) (any, error) {
			<-ctx.Done()
			time.Sleep(time.Second) // ignores the context for a while: the report must not wait
			return "late", nil
		}},
	}
	start := time.Now()
	r := Run(context.Background(), mods, &module.Env{}, Options{Timeout: 100 * time.Millisecond})
	if time.Since(start) > 900*time.Millisecond {
		t.Fatal("engine waited for a module past its timeout")
	}

	want := map[string]model.ModuleStatus{
		"ok":      model.StatusOK,
		"partial": model.StatusPartial,
		"error":   model.StatusError,
		"panic":   model.StatusError,
		"skipped": model.StatusSkipped,
		"slow":    model.StatusError,
	}
	for name, status := range want {
		got, ok := r.Modules[name]
		if !ok {
			t.Fatalf("module %s missing from report", name)
		}
		if got.Status != status {
			t.Errorf("%s: status = %s, want %s (errors %v)", name, got.Status, status, got.Errors)
		}
	}
	if errs := r.Modules["partial"].Errors; len(errs) != 2 || errs[0] != "e1" {
		t.Errorf("partial errors = %v", errs)
	}
	if r.Modules["partial"].Facts != "some" {
		t.Errorf("partial facts lost")
	}
	if r.Modules["skipped"].SkipReason != "no podman" {
		t.Errorf("skip reason = %q", r.Modules["skipped"].SkipReason)
	}
	if r.SchemaVersion != model.SchemaVersion || r.Meta.Tool != "terminus" || r.Meta.Timestamp.IsZero() {
		t.Errorf("meta not filled: %+v", r.Meta)
	}
}

func TestRunChecks(t *testing.T) {
	m := &checkingModule{
		fakeModule: fakeModule{name: "mem", collect: func(context.Context) (any, error) { return 5, nil }},
		check: func(facts any) []model.Finding {
			if facts.(int) != 5 {
				t.Errorf("checker got facts %v", facts)
			}
			return []model.Finding{{ID: "mem.low", Severity: model.SeverityWarn}}
		},
	}
	failing := &checkingModule{
		fakeModule: fakeModule{name: "bad", collect: func(context.Context) (any, error) { return 1, nil }},
		check:      func(any) []model.Finding { panic("checker bug") },
	}
	mods := []module.Module{m, failing}

	r := Run(context.Background(), mods, &module.Env{}, Options{})
	if len(r.Findings) != 0 {
		t.Fatalf("checks ran without Options.Checks: %v", r.Findings)
	}

	r = Run(context.Background(), mods, &module.Env{}, Options{Checks: true})
	if len(r.Findings) != 1 || r.Findings[0].Module != "mem" {
		t.Fatalf("findings = %+v", r.Findings)
	}
	if r.ExitCode() != model.ExitWarn {
		t.Errorf("exit code = %d", r.ExitCode())
	}
	if errs := r.Modules["bad"].Errors; len(errs) != 1 {
		t.Errorf("checker panic not reported: %v", errs)
	}
}

type carrier struct{ facts any }

func (c carrier) ReportFacts() any { return c.facts }

func TestRunDisabledChecksAndCarrier(t *testing.T) {
	m := &checkingModule{
		fakeModule: fakeModule{name: "ext", collect: func(context.Context) (any, error) {
			return carrier{facts: "shown"}, nil
		}},
		check: func(facts any) []model.Finding {
			if _, ok := facts.(carrier); !ok {
				t.Errorf("checker did not get the carrier: %T", facts)
			}
			return []model.Finding{
				{ID: "ext.a", Severity: model.SeverityFail},
				{ID: "ext.b", Severity: model.SeverityWarn},
				{ID: "other.c", Severity: model.SeverityWarn},
			}
		},
	}
	env := &module.Env{Checks: &module.CheckSettings{Disabled: []string{"ext.*"}}}
	r := Run(context.Background(), []module.Module{m}, env, Options{Checks: true})
	if len(r.Findings) != 1 || r.Findings[0].ID != "other.c" {
		t.Fatalf("findings: %+v", r.Findings)
	}
	if r.Modules["ext"].Facts != "shown" {
		t.Fatalf("facts: %#v", r.Modules["ext"].Facts)
	}
}
