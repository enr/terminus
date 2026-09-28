// Package probe stops a service on purpose to verify that the checks meant to notice an outage
// really notice it (section 11 of the srv-01 runbook): the HTTP endpoint goes down, the external
// monitoring reports it, and the service comes back afterwards.
//
// It is the only part of terminus that changes the machine: the service is always started
// again, also when the probe fails or is interrupted.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/runner"
)

// Name is the module name of the probe findings.
const Name = "probe"

// Target is what the probe stops and how it watches.
type Target struct {
	// Unit is a systemd unit; User makes it a unit of that user's manager (systemctl --user).
	Unit string `json:"unit,omitempty"`
	User string `json:"user,omitempty"`
	// Container is a podman container: stopped directly when there is no Unit, otherwise only
	// watched (its health) while the unit is stopped and restarted.
	Container string `json:"container,omitempty"`
	// HTTP is an endpoint that must stop answering while the service is down.
	HTTP string `json:"http,omitempty"`
	// CheckCmd is a shell command that exits 0 when the monitoring noticed the outage.
	CheckCmd string `json:"check_cmd,omitempty"`
	// Wait is how long the service stays down; Recover bounds the wait for it to come back.
	Wait    time.Duration `json:"wait"`
	Recover time.Duration `json:"recover"`
}

// Validate checks the target.
func (t Target) Validate() error {
	if t.Unit == "" && t.Container == "" {
		return errors.New("give --unit or --container: the service to stop")
	}
	if t.Wait <= 0 || t.Recover <= 0 {
		return errors.New("--wait and --recover must be positive")
	}
	return nil
}

// Describe names the service for the confirmation prompt.
func (t Target) Describe() string {
	switch {
	case t.Unit != "" && t.User != "":
		return fmt.Sprintf("the unit %s of user %s", t.Unit, t.User)
	case t.Unit != "":
		return "the unit " + t.Unit
	case t.User != "":
		return fmt.Sprintf("the container %s of user %s", t.Container, t.User)
	default:
		return "the container " + t.Container
	}
}

// Observation is the state of the service at a moment.
type Observation struct {
	Unit       string `json:"unit_state,omitempty"`
	Container  string `json:"container_state,omitempty"`
	Health     string `json:"health,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	HTTPError  string `json:"http_error,omitempty"`
	// CheckExit is the exit code of the check command (-1 when it could not run).
	CheckExit   *int   `json:"check_exit,omitempty"`
	CheckOutput string `json:"check_output,omitempty"`
}

func (o Observation) httpUp() bool {
	return o.HTTPError == "" && o.HTTPStatus > 0 && o.HTTPStatus < 500
}

// Outcome is what happened.
type Outcome struct {
	Target Target      `json:"target"`
	Before Observation `json:"before"`
	During Observation `json:"during"`
	After  Observation `json:"after"`
	// Attempted tells whether the stop was tried (not when interrupted before); Stopped whether
	// it succeeded: otherwise During was not observed.
	Attempted bool `json:"attempted"`
	Stopped   bool `json:"stopped"`
	// Interrupted tells that the wait was cut short: the detection was not verified.
	Interrupted bool     `json:"interrupted"`
	Recovered   bool     `json:"recovered"`
	RecoveryMs  int64    `json:"recovery_ms"`
	Steps       []string `json:"steps"`
	Errors      []string `json:"errors,omitempty"`
}

// Prober runs a probe.
type Prober struct {
	Runner runner.Runner
	Client *http.Client
	// Log receives the progress lines.
	Log func(string)
	// Sleep waits (tests make it instant).
	Sleep func(context.Context, time.Duration) error
	// Poll is the interval of the recovery checks.
	Poll time.Duration
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run stops the target, watches, starts it again and returns the report. The start happens even
// when ctx is canceled (interrupt): it uses its own context bounded by Recover.
func (p *Prober) Run(ctx context.Context, t Target) *model.Report {
	if p.Client == nil {
		p.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if p.Sleep == nil {
		p.Sleep = sleep
	}
	if p.Poll <= 0 {
		p.Poll = 2 * time.Second
	}
	if p.Log == nil {
		p.Log = func(string) {}
	}
	start := time.Now()
	out := &Outcome{Target: t}
	step := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		out.Steps = append(out.Steps, time.Now().UTC().Format("15:04:05")+" "+s)
		p.Log(s)
	}

	out.Before = p.observe(ctx, t)
	step("before: %s", describe(out.Before))

	if err := ctx.Err(); err != nil {
		out.Errors = append(out.Errors, "interrupted before the stop: "+err.Error())
		step("interrupted: nothing stopped")
		return report(out, time.Since(start))
	}

	out.Attempted = true
	step("stopping %s", t.Describe())
	if err := p.command(ctx, t, "stop"); err != nil {
		// It may be half stopped: it is started anyway below.
		out.Errors = append(out.Errors, "stop: "+err.Error())
		step("stop failed: %v", err)
	} else {
		out.Stopped = true
		step("stopped; waiting %s", t.Wait)
		if err := p.Sleep(ctx, t.Wait); err != nil {
			out.Errors = append(out.Errors, "interrupted: "+err.Error())
			out.Interrupted = true
		}
		out.During = p.observe(context.WithoutCancel(ctx), t)
		step("while stopped: %s", describe(out.During))
	}

	// The start uses a context that survives an interrupt of the probe.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.Recover+30*time.Second)
	defer cancel()
	p.restart(rctx, t, out, step)
	return report(out, time.Since(start))
}

// restart starts the service and waits for it to be back.
func (p *Prober) restart(ctx context.Context, t Target, out *Outcome, step func(string, ...any)) {
	step("starting %s", t.Describe())
	begin := time.Now()
	if err := p.command(ctx, t, "start"); err != nil {
		out.Errors = append(out.Errors, "start: "+err.Error())
		step("start failed: %v", err)
	}
	deadline := begin.Add(t.Recover)
	for {
		obs := p.observe(ctx, t)
		if p.recovered(ctx, t, obs) {
			out.After, out.Recovered = obs, true
			out.RecoveryMs = time.Since(begin).Milliseconds()
			step("recovered in %s: %s", time.Since(begin).Round(time.Second), describe(obs))
			return
		}
		if time.Now().After(deadline) {
			out.After = obs
			step("NOT recovered after %s: %s", t.Recover, describe(obs))
			return
		}
		if err := p.Sleep(ctx, p.Poll); err != nil {
			out.After = obs
			return
		}
	}
}

// recovered tells whether the service is back: unit active, container running and healthy (a
// healthcheck is run to avoid waiting for its interval), endpoint answering.
func (p *Prober) recovered(ctx context.Context, t Target, o Observation) bool {
	if t.Unit != "" && o.Unit != "active" {
		return false
	}
	if t.Container != "" {
		if o.Container != "running" {
			return false
		}
		if o.Health != "" && o.Health != "healthy" {
			res, err := p.Runner.Run(ctx, runner.Cmd{Name: "podman", Args: []string{"healthcheck", "run", t.Container}, User: t.User, Timeout: 30 * time.Second})
			if err != nil || res.ExitCode != 0 {
				return false
			}
		}
	}
	if t.HTTP != "" && !o.httpUp() {
		return false
	}
	return true
}

// command stops or starts the unit, or the container when there is no unit.
func (p *Prober) command(ctx context.Context, t Target, action string) error {
	var c runner.Cmd
	if t.Unit != "" {
		args := []string{action, t.Unit}
		if t.User != "" {
			args = append([]string{"--user"}, args...)
		}
		c = runner.Cmd{Name: "systemctl", Args: args, User: t.User, Timeout: 2 * time.Minute}
	} else {
		args := []string{action, t.Container}
		if action == "stop" {
			args = []string{"stop", "--time", "10", t.Container}
		}
		c = runner.Cmd{Name: "podman", Args: args, User: t.User, Timeout: 2 * time.Minute}
	}
	res, err := p.Runner.Run(ctx, c)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: exit code %d: %s", strings.Join(append([]string{c.Name}, c.Args...), " "), res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// observe reads the state of the unit, the container, the endpoint and the check command.
func (p *Prober) observe(ctx context.Context, t Target) Observation {
	var o Observation
	if t.Unit != "" {
		args := []string{"show", "-p", "ActiveState", "--value", t.Unit}
		if t.User != "" {
			args = append([]string{"--user"}, args...)
		}
		if res, err := p.Runner.Run(ctx, runner.Cmd{Name: "systemctl", Args: args, User: t.User}); err == nil {
			o.Unit = strings.TrimSpace(string(res.Stdout))
		}
	}
	if t.Container != "" {
		o.Container, o.Health = "absent", ""
		res, err := p.Runner.Run(ctx, runner.Cmd{Name: "podman", Args: []string{"inspect", "--type", "container", t.Container}, User: t.User})
		if err == nil && res.ExitCode == 0 {
			var raw []struct {
				State struct {
					Status string
					Health *struct{ Status string }
				}
			}
			if json.Unmarshal(res.Stdout, &raw) == nil && len(raw) > 0 {
				o.Container = raw[0].State.Status
				if raw[0].State.Health != nil {
					o.Health = raw[0].State.Health.Status
				}
			}
		}
	}
	if t.HTTP != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.HTTP, nil)
		if err == nil {
			req.Header.Set("User-Agent", "terminus-probe/"+version())
			var res *http.Response
			if res, err = p.Client.Do(req); err == nil {
				o.HTTPStatus = res.StatusCode
				res.Body.Close()
			}
		}
		if err != nil {
			o.HTTPError = err.Error()
		}
	}
	if t.CheckCmd != "" {
		res, err := p.Runner.Run(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", t.CheckCmd}, Timeout: 30 * time.Second})
		code := res.ExitCode
		if err != nil {
			code = -1
		}
		o.CheckExit = &code
		o.CheckOutput = strings.TrimSpace(string(res.Stdout) + string(res.Stderr))
		if len(o.CheckOutput) > 500 {
			o.CheckOutput = o.CheckOutput[:500] + "…"
		}
	}
	return o
}

func version() string {
	if buildinfo.Version != "" {
		return buildinfo.Version
	}
	return "dev"
}

func describe(o Observation) string {
	var parts []string
	if o.Unit != "" {
		parts = append(parts, "unit "+o.Unit)
	}
	if o.Container != "" {
		s := "container " + o.Container
		if o.Health != "" {
			s += " (" + o.Health + ")"
		}
		parts = append(parts, s)
	}
	switch {
	case o.HTTPError != "":
		parts = append(parts, "http error")
	case o.HTTPStatus > 0:
		parts = append(parts, fmt.Sprintf("http %d", o.HTTPStatus))
	}
	if o.CheckExit != nil {
		parts = append(parts, fmt.Sprintf("check exit %d", *o.CheckExit))
	}
	if len(parts) == 0 {
		return "nothing observed"
	}
	return strings.Join(parts, ", ")
}

// report turns the outcome into findings.
func report(out *Outcome, d time.Duration) *model.Report {
	r := model.NewReport()
	r.Meta = model.Meta{Tool: "terminus", Version: buildinfo.Version, Commit: buildinfo.GitCommit, Timestamp: time.Now().UTC(), DurationMs: d.Milliseconds()}
	r.Meta.Hostname, _ = os.Hostname()
	status := model.StatusOK
	if len(out.Errors) > 0 {
		status = model.StatusPartial
	}
	r.Modules[Name] = model.ModuleResult{Name: Name, Status: status, Facts: out, Errors: out.Errors, DurationMs: d.Milliseconds()}

	t := out.Target
	subject := t.Unit
	if subject == "" {
		subject = t.Container
	}
	var fs []model.Finding
	switch {
	case !out.Attempted:
		r.Modules[Name] = model.ModuleResult{Name: Name, Status: model.StatusError, Facts: out, Errors: out.Errors, DurationMs: d.Milliseconds()}
		return r
	case !out.Stopped:
		// Nothing was observed while down: the detection findings would be meaningless.
		fs = append(fs, model.Finding{ID: "probe.stopped", Module: Name, Subject: subject, Severity: model.SeverityFail,
			Message: "could not stop the service: " + strings.Join(out.Errors, "; ")})
	case out.Interrupted:
		fs = append(fs, model.Finding{ID: "probe.interrupted", Module: Name, Subject: subject, Severity: model.SeverityWarn,
			Message: "interrupted before the end of --wait " + t.Wait.String() + ": the detection was not verified"})
	case t.HTTP == "" && t.CheckCmd == "":
		fs = append(fs, model.Finding{ID: "probe.detected-by-check", Module: Name, Subject: subject, Severity: model.SeverityInfo,
			Message: "nothing watched the outage: give --http and/or --check-cmd to verify the monitoring"})
	}
	verify := out.Stopped && !out.Interrupted
	if t.HTTP != "" && verify {
		f := model.Finding{ID: "probe.detected-by-http", Module: Name, Subject: t.HTTP}
		switch {
		case !out.Before.httpUp():
			f.Severity, f.Message = model.SeverityWarn, "the endpoint was already failing before the stop: the probe proves nothing"
		case out.During.httpUp():
			f.Severity = model.SeverityFail
			f.Message = fmt.Sprintf("still answered %d while %s was stopped", out.During.HTTPStatus, subject)
			f.Hint = "another instance or a cache answers for it: a monitor on this URL would not notice the outage"
		default:
			f.Severity, f.Message = model.SeverityOK, "stopped answering while the service was down, as expected"
		}
		fs = append(fs, f)
	}
	if t.CheckCmd != "" && verify {
		f := model.Finding{ID: "probe.detected-by-check", Module: Name, Subject: subject, Evidence: map[string]any{"command": t.CheckCmd}}
		switch {
		case out.During.CheckExit == nil:
			f.Severity, f.Message = model.SeverityFail, "the check command did not run"
		case *out.During.CheckExit == 0:
			f.Severity, f.Message = model.SeverityOK, fmt.Sprintf("the monitoring noticed the outage within %s", t.Wait)
		default:
			f.Severity = model.SeverityFail
			f.Message = fmt.Sprintf("the monitoring did not notice the outage within %s (check exit %d)", t.Wait, *out.During.CheckExit)
			f.Hint = "the check exists on paper only: verify the probe interval, the target and where it reports"
			f.Evidence["output"] = out.During.CheckOutput
		}
		fs = append(fs, f)
	}
	rec := model.Finding{ID: "probe.recovered", Module: Name, Subject: subject}
	if out.Recovered {
		rec.Severity = model.SeverityOK
		rec.Message = fmt.Sprintf("back in %s", (time.Duration(out.RecoveryMs) * time.Millisecond).Round(100*time.Millisecond))
	} else {
		rec.Severity = model.SeverityFail
		rec.Message = "NOT back after " + t.Recover.String() + ": " + describe(out.After)
		rec.Hint = manualStart(t)
	}
	fs = append(fs, rec)
	r.AddFindings(fs...)
	return r
}

func manualStart(t Target) string {
	switch {
	case t.Unit != "" && t.User != "":
		return fmt.Sprintf("start it by hand: systemctl --user -M %s@ start %s", t.User, t.Unit)
	case t.Unit != "":
		return "start it by hand: systemctl start " + t.Unit
	case t.User != "":
		return fmt.Sprintf("start it by hand: sudo -u %s podman start %s", t.User, t.Container)
	default:
		return "start it by hand: podman start " + t.Container
	}
}
