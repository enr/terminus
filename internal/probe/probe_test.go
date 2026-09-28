package probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/runner"
)

// service is a fake machine: systemctl and podman change its state, the check command reads it.
type service struct {
	mu       sync.Mutex
	up       bool
	calls    []string
	stopErr  bool
	startErr bool
	// noticed is the exit code of the check command while the service is down.
	noticed int
	// onStop runs after the stop (to cancel the probe context, for example).
	onStop func()
}

func (s *service) Run(_ context.Context, c runner.Cmd) (runner.Result, error) {
	s.mu.Lock()
	key := runner.Key(c)
	s.calls = append(s.calls, key)
	up := s.up
	var res runner.Result
	var onStop func()
	switch {
	case strings.Contains(key, "systemctl") && strings.Contains(key, " show "):
		res.Stdout = []byte(map[bool]string{true: "active\n", false: "inactive\n"}[up])
	case strings.Contains(key, " stop "):
		if s.stopErr {
			res.ExitCode, res.Stderr = 1, []byte("Access denied")
		} else {
			s.up = false
			onStop = s.onStop
		}
	case strings.Contains(key, " start "):
		if s.startErr {
			res.ExitCode, res.Stderr = 1, []byte("Job failed")
		} else {
			s.up = true
		}
	case strings.Contains(key, "podman inspect"):
		res.Stdout = []byte(map[bool]string{
			true:  `[{"State":{"Status":"running","Health":{"Status":"healthy"}}}]`,
			false: `[{"State":{"Status":"exited","Health":{"Status":"unhealthy"}}}]`,
		}[up])
	case strings.Contains(key, "podman healthcheck run"):
	case strings.HasPrefix(key, "sh -c"):
		if up {
			res.ExitCode = 1
		} else {
			res.ExitCode = s.noticed
		}
	default:
		s.mu.Unlock()
		return res, errors.New("unexpected command " + key)
	}
	s.mu.Unlock()
	if onStop != nil {
		onStop()
	}
	return res, nil
}

func (s *service) Stream(context.Context, runner.Cmd, func([]byte) error) (runner.Result, error) {
	return runner.Result{}, errors.New("not used")
}

func (s *service) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func (s *service) isUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.up
}

// actions returns the stop and start commands, in order.
func (s *service) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if strings.Contains(c, " stop ") || strings.Contains(c, " start ") {
			out = append(out, c)
		}
	}
	return out
}

func prober(s *service) *Prober {
	return &Prober{
		Runner: s,
		Poll:   time.Millisecond,
		Sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
				return nil
			}
		},
	}
}

func target() Target {
	return Target{Unit: "app.service", User: "apps", Wait: time.Second, Recover: 200 * time.Millisecond}
}

func finding(t *testing.T, r *model.Report, id string) model.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("finding %s missing: %+v", id, r.Findings)
	return model.Finding{}
}

func TestProbeStopsWatchesAndStarts(t *testing.T) {
	s := &service{up: true, noticed: 0}
	// The endpoint follows the service.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !s.isUp() {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	tg := target()
	tg.Container, tg.HTTP, tg.CheckCmd = "app", srv.URL, "grep -q DOWN /var/log/easeprobe.log"

	r := prober(s).Run(context.Background(), tg)

	want := []string{"apps|systemctl --user stop app.service", "apps|systemctl --user start app.service"}
	if got := s.actions(); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("actions = %q, want %q", got, want)
	}
	for _, id := range []string{"probe.detected-by-http", "probe.detected-by-check", "probe.recovered"} {
		if f := finding(t, r, id); f.Severity != model.SeverityOK {
			t.Errorf("%s = %s %q, want ok", id, f.Severity, f.Message)
		}
	}
	out := r.Modules[Name].Facts.(*Outcome)
	if out.During.Unit != "inactive" || out.During.Health != "unhealthy" || out.During.HTTPStatus != 502 {
		t.Errorf("during = %+v", out.During)
	}
	if r.ExitCode() != model.ExitOK {
		t.Errorf("exit code = %d", r.ExitCode())
	}
}

func TestProbeDetectsBlindMonitoring(t *testing.T) {
	s := &service{up: true, noticed: 1}
	// Something else keeps answering on the URL.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	tg := target()
	tg.HTTP, tg.CheckCmd = srv.URL, "false"

	r := prober(s).Run(context.Background(), tg)
	if f := finding(t, r, "probe.detected-by-http"); f.Severity != model.SeverityFail {
		t.Errorf("http = %s %q", f.Severity, f.Message)
	}
	if f := finding(t, r, "probe.detected-by-check"); f.Severity != model.SeverityFail || f.Hint == "" {
		t.Errorf("check = %s %q", f.Severity, f.Message)
	}
	if f := finding(t, r, "probe.recovered"); f.Severity != model.SeverityOK {
		t.Errorf("recovered = %s %q", f.Severity, f.Message)
	}
}

func TestProbeStartsAfterAFailedStop(t *testing.T) {
	s := &service{up: true, stopErr: true}
	r := prober(s).Run(context.Background(), target())
	if got := s.actions(); len(got) != 2 || !strings.Contains(got[1], " start ") {
		t.Fatalf("actions = %q, want stop then start", got)
	}
	if f := finding(t, r, "probe.stopped"); f.Severity != model.SeverityFail || !strings.Contains(f.Message, "Access denied") {
		t.Errorf("stopped = %s %q", f.Severity, f.Message)
	}
	for _, f := range r.Findings {
		if strings.HasPrefix(f.ID, "probe.detected") {
			t.Errorf("unexpected %s after a failed stop", f.ID)
		}
	}
}

func TestProbeStartsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &service{up: true, onStop: cancel}
	tg := target()
	tg.Wait, tg.CheckCmd = time.Hour, "true"
	done := make(chan *model.Report)
	go func() { done <- prober(s).Run(ctx, tg) }()
	var r *model.Report
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe did not return after the interrupt")
	}
	if !s.isUp() {
		t.Fatalf("the service was left down; calls: %q", s.calls)
	}
	if f := finding(t, r, "probe.recovered"); f.Severity != model.SeverityOK {
		t.Errorf("recovered = %s %q", f.Severity, f.Message)
	}
	if f := finding(t, r, "probe.interrupted"); f.Severity != model.SeverityWarn {
		t.Errorf("interrupted = %s %q", f.Severity, f.Message)
	}
	for _, f := range r.Findings {
		if strings.HasPrefix(f.ID, "probe.detected") {
			t.Errorf("unexpected %s after an interrupt", f.ID)
		}
	}
}

func TestProbeDoesNothingWhenInterruptedBefore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &service{up: true}
	r := prober(s).Run(ctx, target())
	if got := s.actions(); len(got) != 0 {
		t.Fatalf("actions = %q, want none", got)
	}
	if r.Modules[Name].Status != model.StatusError {
		t.Errorf("status = %s", r.Modules[Name].Status)
	}
}

func TestProbeNotRecovered(t *testing.T) {
	s := &service{up: true, startErr: true}
	var polls atomic.Int32
	p := prober(s)
	sleep := p.Sleep
	p.Sleep = func(ctx context.Context, d time.Duration) error { polls.Add(1); return sleep(ctx, d) }
	tg := target()
	tg.Unit, tg.User, tg.Container = "", "", "web"
	r := p.Run(context.Background(), tg)
	f := finding(t, r, "probe.recovered")
	if f.Severity != model.SeverityFail || f.Hint != "start it by hand: podman start web" {
		t.Errorf("recovered = %s %q hint %q", f.Severity, f.Message, f.Hint)
	}
	if want := []string{"podman stop --time 10 web", "podman start web"}; strings.Join(s.actions(), ";") != strings.Join(want, ";") {
		t.Errorf("actions = %q", s.actions())
	}
	if polls.Load() < 2 {
		t.Errorf("recovery polled %d times", polls.Load())
	}
	if f := finding(t, r, "probe.detected-by-check"); f.Severity != model.SeverityInfo {
		t.Errorf("without --http/--check-cmd = %s", f.Severity)
	}
}

func TestValidateAndDescribe(t *testing.T) {
	if err := (Target{Wait: time.Second, Recover: time.Second}).Validate(); err == nil {
		t.Error("a target without unit or container is valid")
	}
	if err := (Target{Unit: "a", Recover: time.Second}).Validate(); err == nil {
		t.Error("a zero wait is valid")
	}
	if err := target().Validate(); err != nil {
		t.Error(err)
	}
	for tg, want := range map[Target]string{
		{Unit: "a.service"}:                 "the unit a.service",
		{Unit: "a.service", User: "u"}:      "the unit a.service of user u",
		{Container: "c"}:                    "the container c",
		{Container: "c", User: "u"}:         "the container c of user u",
		{Unit: "a.service", Container: "c"}: "the unit a.service",
	} {
		if got := tg.Describe(); got != want {
			t.Errorf("%+v: %q, want %q", tg, got, want)
		}
	}
}
