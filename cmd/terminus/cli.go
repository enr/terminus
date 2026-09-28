package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/engine"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/modules/external"
	"github.com/enr/terminus/internal/modules/system"
	"github.com/enr/terminus/internal/output"
	"github.com/enr/terminus/internal/query"
)

// globalOptions are the flags shared by every command.
type globalOptions struct {
	debug            bool
	timeout          time.Duration
	color            string
	configPath       string
	externalFactsDir string
	modulesDir       string
	only             []string
	enable           []string
	disable          []string
	users            []string
	noPager          bool
	// changed tells whether a flag was set on the command line (flags win over the configuration).
	changed func(name string) bool
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
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		g.changed = func(name string) bool {
			f := cmd.Flags().Lookup(name)
			return f != nil && f.Changed
		}
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
	pf.StringVar(&g.configPath, "config", config.DefaultPath, "configuration file; bypasses the system/user/local layering (optional unless given)")
	pf.StringVar(&g.externalFactsDir, "external-facts-dir", external.DefaultDir, "path to the external facts directory")
	pf.StringVar(&g.modulesDir, "modules-dir", config.DefaultModulesDir, "path to the external modules directory")
	pf.StringSliceVar(&g.only, "only", nil, "run only these modules")
	pf.StringSliceVar(&g.enable, "modules", nil, "enable these modules too")
	pf.StringSliceVar(&g.disable, "no-modules", nil, "disable these modules")
	pf.BoolVar(&g.noPager, "no-pager", false, "do not page long output on a terminal (pager: $TERMINUS_PAGER, $PAGER or less)")
	pf.StringSliceVar(&g.users, "users", nil, "users inspected by systemd, podman, quadlet and timers (default from the configuration: auto)")

	for _, name := range []string{"only", "modules", "no-modules"} {
		_ = root.RegisterFlagCompletionFunc(name, completeModules(g))
	}

	// Defined here to keep -v free: subcommands use it for --verbose.
	root.Flags().Bool("version", false, "print the version")
	addFactsFlags(root, fo)
	root.AddCommand(
		newFactsCmd(g, stdout, stderr),
		newCheckCmd(g, stdout),
		newReportCmd(g, stdout),
		newDiffCmd(stdout),
		newRemoteCmd(g, stdout),
		newProbeCmd(g, stdout),
		newServeCmd(g, stderr),
		newModulesCmd(g, stdout, stderr),
		newChecksCmd(g, stdout, stderr),
		newConfigCmd(g, stdout, stderr),
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
	// Only v1 command lines, which have no subcommand: "terminus probe --http URL" stays as is.
	if serve && strings.HasPrefix(out[0], "-") {
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

func renderOptions(g *globalOptions, stdout io.Writer) (output.Options, error) {
	o := output.Options{}
	f, ok := stdout.(*os.File)
	if !ok {
		// Not a file (tests, buffers): colors only when explicitly asked.
		o.Color = g.color == string(output.ColorAlways)
		return o, nil
	}
	color, err := output.UseColor(output.ColorMode(g.color), f)
	o.Color, o.Width = color, terminalWidth(f)
	return o, err
}

// terminalWidth returns the width of w when it is a terminal, 0 otherwise.
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}

type factsOptions struct {
	output     string
	outputFile string
	verbose    bool
	format     string
	formatFile string
}

func addFactsFlags(cmd *cobra.Command, fo *factsOptions) {
	f := cmd.Flags()
	f.StringVarP(&fo.output, "output", "o", "text", "output format: "+strings.Join(output.Formats, ", "))
	f.StringVar(&fo.outputFile, "output-file", "", "write to this file (atomically) instead of the standard output")
	f.BoolVarP(&fo.verbose, "verbose", "v", false, "show long lists and tables in full (text output)")
	f.StringVar(&fo.format, "format", "", "format the facts with the given Go template")
	f.StringVar(&fo.formatFile, "format-file", "", "format the facts with the Go template in the given file")
}

func newFactsCmd(g *globalOptions, stdout, stderr io.Writer) *cobra.Command {
	fo := &factsOptions{}
	cmd := &cobra.Command{
		Use:   "facts [path]",
		Short: "Print the facts about the machine",
		Long: `Print the facts about the machine.

Lists of records (interfaces, filesystems, units ...) are shown as tables of their main
fields; -v shows every field.

With a path only that value is printed: a section as text on a terminal and as JSON otherwise
(-o json or -o text to choose), a single value as is. Elements of lists are selected by name
(network.interfaces.eth0, storage.filesystems./srv) or by position.`,
		Example: `  terminus facts
  terminus facts -o json
  terminus facts storage
  terminus facts network.interfaces.eth0
  terminus facts system.kernel.release
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
	r, err := a.collect(ctx, false)
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
		v, schema, ok := query.ResolveSchema(tree, args[0])
		if !ok {
			return &exitError{code: model.ExitError, err: fmt.Errorf("fact %q not found", args[0])}
		}
		if factsValueAsText(g, fo, stdout, v) {
			o, err := renderOptions(g, stdout)
			if err != nil {
				return err
			}
			o.Verbose, o.Tables = fo.verbose, module.Tables(a.reg.All())
			return page(g, stdout, func(w io.Writer) error { return output.RenderFactsValue(w, args[0], schema, v, o) })
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
	o.Facts, o.Verbose, o.Tables = true, fo.verbose, module.Tables(a.reg.All())
	if fo.outputFile != "" {
		o.Color, o.Width = false, 0
		return writeOutput(fo.outputFile, stdout, func(w io.Writer) error { return renderer.Render(w, r, o) })
	}
	return page(g, stdout, func(w io.Writer) error { return renderer.Render(w, r, o) })
}

// factsValueAsText tells whether a section of the facts is printed as text rather than JSON:
// when asked with -o text, or on a terminal unless another format is asked. Single values are
// printed as they are, for scripts.
func factsValueAsText(g *globalOptions, fo *factsOptions, stdout io.Writer, v any) bool {
	switch v.(type) {
	case map[string]any, []any:
	default:
		return false
	}
	if g.changed != nil && g.changed("output") {
		return fo.output == "text"
	}
	f, ok := stdout.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
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
	output     string
	outputFile string
	verbose    bool
	problems   bool
	facts      bool
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
  terminus check -o json | jq .summary
  terminus check -o prometheus --output-file /var/lib/node_exporter/textfile/terminus.prom`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, g, co, stdout)
		},
	}
	addCheckFlags(cmd, co, "text")
	return cmd
}

func newReportCmd(g *globalOptions, stdout io.Writer) *cobra.Command {
	co := &checkOptions{facts: true}
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Write a complete report: checks and facts",
		Long: `Write a complete report of the machine: summary, findings, modules and facts.

A snapshot to keep before and after a deploy or during an incident; save it as JSON to compare
two snapshots with "terminus diff". The exit code is the one of "terminus check".`,
		Example: `  terminus report > srv-01.md
  terminus report -o html --output-file /tmp/srv-01.html
  terminus report -o json --output-file before.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, g, co, stdout)
		},
	}
	addCheckFlags(cmd, co, "markdown")
	return cmd
}

func addCheckFlags(cmd *cobra.Command, co *checkOptions, defaultFormat string) {
	f := cmd.Flags()
	f.StringVarP(&co.output, "output", "o", defaultFormat, "output format: "+strings.Join(output.Formats, ", "))
	f.StringVar(&co.outputFile, "output-file", "", "write to this file (atomically) instead of the standard output")
	f.BoolVarP(&co.verbose, "verbose", "v", false, "show evidence and hints of the findings (text output)")
	f.BoolVar(&co.problems, "problems", false, "show only warn and fail findings")
}

func runCheck(cmd *cobra.Command, g *globalOptions, co *checkOptions, stdout io.Writer) error {
	renderer, err := output.ForFormat(co.output)
	if err != nil {
		return err
	}
	a, err := newApp(g, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	r, err := a.collect(cmd.Context(), true)
	if err != nil {
		return err
	}
	o, err := renderOptions(g, stdout)
	if err != nil {
		return err
	}
	o.Findings, o.Verbose, o.ProblemsOnly, o.Facts = true, co.verbose, co.problems, co.facts
	o.Tables = module.Tables(a.reg.All())
	render := func(w io.Writer) error { return renderer.Render(w, r, o) }
	if co.outputFile != "" {
		o.Color, o.Width = false, 0
		err = writeOutput(co.outputFile, stdout, render)
	} else {
		err = page(g, stdout, render)
	}
	if err != nil {
		return err
	}
	if code := r.ExitCode(); code != model.ExitOK {
		return &exitError{code: code}
	}
	return nil
}

// writeOutput writes to stdout, or to path through a temporary file renamed at the end: readers
// (node_exporter, a browser) never see a half-written file.
func writeOutput(path string, stdout io.Writer, write func(io.Writer) error) error {
	if path == "" {
		return write(stdout)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// completeModules completes the comma-separated module names of --only, --modules and --no-modules,
// external modules included. The selection flags are ignored: the names are those of the registry.
func completeModules(g *globalOptions) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		probe := *g
		probe.only, probe.enable, probe.disable = nil, nil, nil
		probe.changed = func(name string) bool {
			f := cmd.Flags().Lookup(name)
			return f != nil && f.Changed
		}
		a, err := newApp(&probe, io.Discard)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		// Names already in the list are not proposed again.
		prefix := ""
		done := map[string]bool{}
		if i := strings.LastIndex(toComplete, ","); i >= 0 {
			prefix = toComplete[:i+1]
			for _, n := range strings.Split(prefix, ",") {
				done[n] = true
			}
		}
		var out []string
		for _, m := range a.reg.All() {
			if n := m.Name(); !done[n] {
				out = append(out, prefix+n+"\t"+m.Description())
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}
