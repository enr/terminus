package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/engine"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/modules/cpu"
	"github.com/enr/terminus/internal/modules/external"
	"github.com/enr/terminus/internal/modules/memory"
	"github.com/enr/terminus/internal/modules/network"
	"github.com/enr/terminus/internal/modules/storage"
	"github.com/enr/terminus/internal/modules/system"
	"github.com/enr/terminus/internal/output"
	"github.com/enr/terminus/internal/query"
	"github.com/enr/terminus/internal/runner"
)

// globalOptions are the flags shared by every command.
type globalOptions struct {
	debug            bool
	timeout          time.Duration
	color            string
	externalFactsDir string
}

// exitError carries a specific exit code up to run.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit code %d", e.code)
	}
	return e.err.Error()
}

// run executes the CLI and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	g := &globalOptions{}
	root := newRootCmd(g, stdout, stderr)
	root.SetArgs(normalizeLegacyArgs(args))
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return model.ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(stderr, "terminus:", ee.err)
		}
		return ee.code
	}
	fmt.Fprintln(stderr, "terminus:", err)
	return model.ExitError
}

func newRootCmd(g *globalOptions, stdout, stderr io.Writer) *cobra.Command {
	fo := &factsOptions{}
	root := &cobra.Command{
		Use:   "terminus [path]",
		Short: "Report the state of a Linux machine: facts and checks",
		Long: `terminus collects facts about a Linux machine and evaluates them.

Without a subcommand it behaves like "terminus facts", as terminus v1 did.`,
		Version:       versionString(),
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFacts(cmd.Context(), g, fo, args, stdout, stderr)
		},
	}
	root.SetVersionTemplate("terminus {{.Version}}\n")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		switch output.ColorMode(g.color) {
		case output.ColorAuto, output.ColorAlways, output.ColorNever:
			return nil
		}
		return fmt.Errorf("invalid --color %q (auto, always, never)", g.color)
	}

	pf := root.PersistentFlags()
	pf.BoolVar(&g.debug, "debug", false, "log collection errors and diagnostics to stderr")
	pf.DurationVar(&g.timeout, "timeout", engine.DefaultTimeout, "maximum time for each module")
	pf.StringVar(&g.color, "color", string(output.ColorAuto), "use colors: auto, always, never")
	pf.StringVar(&g.externalFactsDir, "external-facts-dir", external.DefaultDir, "path to the external facts directory")

	// Defined here to keep -v free: subcommands use it for --verbose.
	root.Flags().Bool("version", false, "print the version")
	addFactsFlags(root, fo)
	root.AddCommand(
		newFactsCmd(g, stdout, stderr),
		newCheckCmd(g, stdout),
		newServeCmd(g, stderr),
		newVersionCmd(stdout),
	)
	return root
}

// legacyFlag matches the single-dash long flags of terminus v1 (-version, -http :6060, ...).
var legacyFlag = regexp.MustCompile(`^-(debug|version|http|format|format-file|external-facts-dir)(=.*)?$`)

// normalizeLegacyArgs keeps terminus v1 command lines working: single-dash long flags become
// double-dash ones and "-http addr" becomes "serve --http addr".
func normalizeLegacyArgs(args []string) []string {
	out := make([]string, 0, len(args))
	serve := false
	for _, a := range args {
		if legacyFlag.MatchString(a) {
			a = "-" + a
		}
		if a == "--http" || strings.HasPrefix(a, "--http=") {
			serve = true
		}
		out = append(out, a)
	}
	if serve && (len(out) == 0 || out[0] != "serve") {
		out = append([]string{"serve"}, out...)
	}
	return out
}

func versionString() string {
	v := buildinfo.Version
	if v == "" {
		v = "dev"
	}
	s := v
	if buildinfo.GitCommit != "" {
		s += "\nRevision: " + buildinfo.GitCommit
	}
	if buildinfo.BuildTime != "" {
		s += "\nBuild date: " + buildinfo.BuildTime
	}
	return s
}

func newVersionCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintf(stdout, "terminus %s\n", versionString())
		},
	}
}

// app holds what the commands share once the flags are parsed.
type app struct {
	g   *globalOptions
	reg *module.Registry
	env *module.Env
}

// newApp prepares the module registry and the environment.
func newApp(g *globalOptions, stderr io.Writer) (*app, error) {
	level := slog.LevelWarn
	if g.debug {
		level = slog.LevelDebug
	}
	env := &module.Env{
		Runner: runner.Exec{},
		Debug:  g.debug,
		Log:    slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})),
	}
	reg, err := module.NewRegistry(
		system.New(),
		cpu.New(),
		memory.New(),
		storage.New(),
		network.New(),
		external.New(g.externalFactsDir),
	)
	if err != nil {
		return nil, err
	}
	return &app{g: g, reg: reg, env: env}, nil
}

// collect runs the selected modules (the core ones when only is empty).
func (a *app) collect(ctx context.Context, only []string, checks bool) (*model.Report, error) {
	mods, err := a.reg.Select(only)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return engine.Run(ctx, mods, a.env, engine.Options{Timeout: a.g.timeout, Checks: checks}), nil
}

func renderOptions(g *globalOptions, stdout io.Writer) (output.Options, error) {
	o := output.Options{}
	f, ok := stdout.(*os.File)
	if !ok {
		// Not a file (tests, buffers): colors only when explicitly asked.
		o.Color = g.color == string(output.ColorAlways)
		return o, nil
	}
	color, err := output.UseColor(output.ColorMode(g.color), f)
	o.Color = color
	return o, err
}

type factsOptions struct {
	output     string
	verbose    bool
	only       []string
	format     string
	formatFile string
}

func addFactsFlags(cmd *cobra.Command, fo *factsOptions) {
	f := cmd.Flags()
	f.StringVarP(&fo.output, "output", "o", "text", "output format: text, json")
	f.BoolVarP(&fo.verbose, "verbose", "v", false, "show long lists and tables in full (text output)")
	f.StringSliceVar(&fo.only, "only", nil, "run only these modules (default: core modules)")
	f.StringVar(&fo.format, "format", "", "format the facts with the given Go template")
	f.StringVar(&fo.formatFile, "format-file", "", "format the facts with the Go template in the given file")
}

func newFactsCmd(g *globalOptions, stdout, stderr io.Writer) *cobra.Command {
	fo := &factsOptions{}
	cmd := &cobra.Command{
		Use:   "facts [path]",
		Short: "Print the facts about the machine",
		Long: `Print the facts about the machine.

With a path (e.g. System.Memory.Total) only that value is printed.`,
		Example: `  terminus facts
  terminus facts -o json
  terminus facts System.Kernel.Release
  terminus facts --format 'Machine ID is {{ .System.MachineID }}'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFacts(cmd.Context(), g, fo, args, stdout, stderr)
		},
	}
	addFactsFlags(cmd, fo)
	return cmd
}

func runFacts(ctx context.Context, g *globalOptions, fo *factsOptions, args []string, stdout, stderr io.Writer) error {
	renderer, err := output.ForFormat(fo.output)
	if err != nil {
		return err
	}
	a, err := newApp(g, stderr)
	if err != nil {
		return err
	}
	r, err := a.collect(ctx, fo.only, false)
	if err != nil {
		return err
	}

	switch {
	case fo.format != "" || fo.formatFile != "":
		return executeTemplate(fo, r, stdout)
	case len(args) == 1:
		tree, err := query.Generic(r.FactsTree())
		if err != nil {
			return err
		}
		v, ok := query.Resolve(tree, args[0])
		if !ok {
			return &exitError{code: model.ExitError, err: fmt.Errorf("fact %q not found", args[0])}
		}
		s, err := query.Format(v)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, s)
		return nil
	}

	o, err := renderOptions(g, stdout)
	if err != nil {
		return err
	}
	o.Facts, o.Verbose = true, fo.verbose
	return renderer.Render(stdout, r, o)
}

// templateData is what templates see: facts keyed by module name, plus the names used by
// terminus v1 ("System" and the external facts at the top level) so old templates keep working.
func templateData(r *model.Report) map[string]any {
	data := r.FactsTree()
	if sys, ok := data[system.Name]; ok {
		data["System"] = sys
	}
	if ext, ok := data[external.Name].(map[string]any); ok {
		for k, v := range ext {
			if _, taken := data[k]; !taken {
				data[k] = v
			}
		}
	}
	return data
}

func executeTemplate(fo *factsOptions, r *model.Report, stdout io.Writer) error {
	var (
		tmpl *template.Template
		err  error
	)
	if fo.format != "" {
		tmpl, err = template.New("format").Parse(fo.format)
	} else {
		tmpl, err = template.ParseFiles(fo.formatFile)
	}
	if err != nil {
		return err
	}
	return tmpl.Execute(stdout, templateData(r))
}

type checkOptions struct {
	output   string
	only     []string
	verbose  bool
	problems bool
}

func newCheckCmd(g *globalOptions, stdout io.Writer) *cobra.Command {
	co := &checkOptions{}
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Collect the facts and evaluate them",
		Long: `Collect the facts and evaluate them.

Exit code: 0 all good, 1 warnings, 2 failures, 3 terminus error.`,
		Example: `  terminus check
  terminus check -v --problems
  terminus check -o json | jq .summary`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			renderer, err := output.ForFormat(co.output)
			if err != nil {
				return err
			}
			a, err := newApp(g, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			r, err := a.collect(cmd.Context(), co.only, true)
			if err != nil {
				return err
			}
			o, err := renderOptions(g, stdout)
			if err != nil {
				return err
			}
			o.Findings, o.Verbose, o.ProblemsOnly = true, co.verbose, co.problems
			if err := renderer.Render(stdout, r, o); err != nil {
				return err
			}
			if code := r.ExitCode(); code != model.ExitOK {
				return &exitError{code: code}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&co.output, "output", "o", "text", "output format: text, json")
	f.StringSliceVar(&co.only, "only", nil, "run only these modules (default: core modules)")
	f.BoolVarP(&co.verbose, "verbose", "v", false, "show evidence and hints of the findings")
	f.BoolVar(&co.problems, "problems", false, "show only warn and fail findings")
	return cmd
}
