// Package engine runs modules in parallel and assembles their results into a report.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// DefaultTimeout is the time a single module is given to collect its facts.
const DefaultTimeout = 30 * time.Second

// Options tune a run.
type Options struct {
	// Timeout per module; DefaultTimeout when zero.
	Timeout time.Duration
	// Checks enables the evaluation of the facts.
	Checks bool
}

// Run collects the facts of every module in parallel and, if requested, evaluates them.
// A failing, panicking or hanging module never stops the others: its error ends up in the report.
func Run(ctx context.Context, mods []module.Module, env *module.Env, opts Options) *model.Report {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	start := time.Now()
	r := model.NewReport()
	r.Meta = model.Meta{
		Tool:      "terminus",
		Version:   buildinfo.Version,
		Commit:    buildinfo.GitCommit,
		Timestamp: start.UTC(),
	}
	r.Meta.Hostname, _ = os.Hostname()

	type outcome struct {
		result   model.ModuleResult
		findings []model.Finding
	}
	outcomes := make([]outcome, len(mods))
	var wg sync.WaitGroup
	for i, m := range mods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, facts := collect(ctx, m, env, timeout)
			o := outcome{result: res}
			if opts.Checks && res.Facts != nil {
				if c, ok := m.(module.Checker); ok {
					fs, err := check(c, env, facts)
					if err != nil {
						o.result.Errors = append(o.result.Errors, err.Error())
					}
					for _, f := range fs {
						if env != nil && env.Checks.IsDisabled(f.ID) {
							continue
						}
						f.Module = m.Name()
						o.findings = append(o.findings, f)
					}
				}
			}
			if c, ok := o.result.Facts.(module.Carrier); ok {
				o.result.Facts = c.ReportFacts()
			}
			outcomes[i] = o
		}()
	}
	wg.Wait()

	for _, o := range outcomes {
		r.Modules[o.result.Name] = o.result
		r.AddFindings(o.findings...)
	}
	r.Meta.DurationMs = time.Since(start).Milliseconds()
	return r
}

// collect runs a module with a timeout. The module keeps running in its goroutine if it ignores
// the context, but the report does not wait for it.
func collect(ctx context.Context, m module.Module, env *module.Env, timeout time.Duration) (model.ModuleResult, any) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type done struct {
		facts any
		err   error
	}
	ch := make(chan done, 1)
	start := time.Now()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				if env != nil && env.Debug && env.Log != nil {
					env.Log.Error("module panic", "module", m.Name(), "panic", p, "stack", string(debug.Stack()))
				}
				ch <- done{err: fmt.Errorf("panic: %v", p)}
			}
		}()
		facts, err := m.Collect(ctx, env)
		ch <- done{facts: facts, err: err}
	}()

	res := model.ModuleResult{Name: m.Name()}
	var d done
	select {
	case d = <-ch:
	case <-ctx.Done():
		d = done{err: fmt.Errorf("collection aborted: %w", ctx.Err())}
	}
	res.DurationMs = time.Since(start).Milliseconds()
	res.Facts = d.facts

	var skip *module.SkipError
	switch {
	case errors.As(d.err, &skip):
		res.Status = model.StatusSkipped
		res.SkipReason = skip.Reason
		res.Facts = nil
	case d.err != nil && d.facts != nil:
		res.Status = model.StatusPartial
		res.Errors = errorStrings(d.err)
	case d.err != nil:
		res.Status = model.StatusError
		res.Errors = errorStrings(d.err)
	default:
		res.Status = model.StatusOK
	}
	return res, d.facts
}

func check(c module.Checker, env *module.Env, facts any) (fs []model.Finding, err error) {
	defer func() {
		if p := recover(); p != nil {
			fs, err = nil, fmt.Errorf("check panic: %v", p)
		}
	}()
	return c.Check(env, facts), nil
}

// errorStrings flattens joined errors (errors.Join) into one string each.
func errorStrings(err error) []string {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []string
		for _, e := range j.Unwrap() {
			out = append(out, errorStrings(e)...)
		}
		return out
	}
	return []string{err.Error()}
}
