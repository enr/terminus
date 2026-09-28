package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/output"
)

// minWrap is the least room for the last column to wrap beside the others; with less it goes on
// lines of its own.
const minWrap = 30

// printTable prints rows aligned in columns. When the table is wider than the terminal the last
// column (a description) wraps beside the others or, lacking room, below its row.
func printTable(w io.Writer, header []string, rows [][]string) {
	printTableWidth(w, terminalWidth(w), header, rows)
}

// printTableWidth is printTable for a terminal width columns wide, 0 for no limit.
func printTableWidth(w io.Writer, width int, header []string, rows [][]string) {
	all := append([][]string{header}, rows...)
	last := len(header) - 1
	widths := make([]int, last)
	lastWidth := 0
	for _, r := range all {
		for j := range last {
			widths[j] = max(widths[j], lipgloss.Width(r[j]))
		}
		lastWidth = max(lastWidth, lipgloss.Width(r[last]))
	}
	prefix := 0
	for _, n := range widths {
		prefix += n + 2
	}
	room := 0 // no wrapping
	if width > 0 && prefix+lastWidth > width {
		room = width - prefix
		if room < minWrap {
			room = -(width - 4) // below the row
		}
	}
	for i, r := range all {
		var b strings.Builder
		for j := range last {
			b.WriteString(r[j] + strings.Repeat(" ", widths[j]-lipgloss.Width(r[j])+2))
		}
		var more []string
		switch {
		case room > 0:
			lines := wrap(r[last], room)
			b.WriteString(lines[0])
			for _, l := range lines[1:] {
				more = append(more, strings.Repeat(" ", prefix)+l)
			}
		case room < 0 && i > 0:
			for _, l := range wrap(r[last], -room) {
				more = append(more, "    "+l)
			}
		case room < 0:
			// The header of a column shown below the rows is left out.
		default:
			b.WriteString(r[last])
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
		for _, l := range more {
			fmt.Fprintln(w, l)
		}
	}
}

// wrap splits text into lines of at most width columns, at spaces when possible.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		for lipgloss.Width(word) > width {
			r := []rune(word)
			if line != "" {
				lines, line = append(lines, line), ""
			}
			lines, word = append(lines, string(r[:width])), string(r[width:])
		}
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines, line = append(lines, line), word
		}
	}
	return append(lines, line)
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newModulesCmd(g *globalOptions, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "modules",
		Short: "List and detect modules",
	}
	var format string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the modules, whether they run and why",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			a, err := newApp(g, stderr)
			if err != nil {
				return err
			}
			states, err := a.reg.Resolve(a.sel)
			if err != nil {
				return err
			}
			type row struct {
				Name        string `json:"name"`
				Kind        string `json:"kind"`
				Enabled     bool   `json:"enabled"`
				Reason      string `json:"reason"`
				Description string `json:"description"`
			}
			var rows []row
			for _, s := range states {
				rows = append(rows, row{s.Module.Name(), kind(s.Module), s.Enabled, s.Reason, s.Module.Description()})
			}
			if format == "json" {
				return printJSON(stdout, rows)
			}
			var table [][]string
			for _, r := range rows {
				on := "no"
				if r.Enabled {
					on = "yes"
				}
				table = append(table, []string{r.Name, r.Kind, on, r.Reason, r.Description})
			}
			printTable(stdout, []string{"MODULE", "KIND", "ENABLED", "WHY", "DESCRIPTION"}, table)
			return nil
		},
	}
	list.Flags().StringVarP(&format, "output", "o", "text", "output format: text, json")

	detect := &cobra.Command{
		Use:   "detect",
		Short: "Tell which optional modules fit this machine and suggest their configuration",
		Long: `Tell which optional modules fit this machine and suggest their configuration.

Nothing is enabled: copy the suggested sections into terminus.toml.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp(g, stderr)
			if err != nil {
				return err
			}
			states, err := a.reg.Resolve(a.sel)
			if err != nil {
				return err
			}
			var table [][]string
			var suggest []string
			for _, s := range states {
				m := s.Module
				if m.Core() {
					continue
				}
				d, ok := m.(module.Detector)
				if !ok {
					table = append(table, []string{m.Name(), "-", "no detection available"})
					continue
				}
				det := d.Detect(cmd.Context(), a.env)
				found := "no"
				if det.Found {
					found = "yes"
					if !s.Enabled && det.Config != "" {
						suggest = append(suggest, fmt.Sprintf("[modules.%s]\n%s\n", m.Name(), det.Config))
					}
				}
				table = append(table, []string{m.Name(), found, det.Reason})
			}
			printTable(stdout, []string{"MODULE", "FOUND", "DETAILS"}, table)
			if len(suggest) > 0 {
				fmt.Fprintf(stdout, "\nSuggested configuration (%s):\n\n%s", a.configPathHint(), strings.Join(suggest, "\n"))
			} else {
				fmt.Fprintln(stdout, "\nNo optional module to enable.")
			}
			return nil
		},
	}
	cmd.AddCommand(list, detect)
	return cmd
}

func kind(m module.Module) string {
	if e, ok := m.(module.External); ok && e.External() {
		return "external"
	}
	if m.Core() {
		return "core"
	}
	return "optional"
}

func (a *app) configPathHint() string {
	if a.cfg.Path != "" {
		return a.cfg.Path
	}
	return "terminus.toml"
}

func newChecksCmd(g *globalOptions, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checks",
		Short: "List the checks",
	}
	var format string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the checks with their effective thresholds",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			a, err := newApp(g, stderr)
			if err != nil {
				return err
			}
			type row struct {
				ID          string            `json:"id"`
				Module      string            `json:"module"`
				Description string            `json:"description"`
				Threshold   *module.Threshold `json:"threshold,omitempty"`
				Configured  bool              `json:"threshold_configured,omitempty"`
				Disabled    bool              `json:"disabled,omitempty"`
			}
			var rows []row
			for _, m := range a.reg.All() {
				c, ok := m.(module.Checker)
				if !ok {
					continue
				}
				for _, ci := range c.Checks() {
					r := row{ID: ci.ID, Module: m.Name(), Description: ci.Description, Disabled: a.cfg.Checks.IsDisabled(ci.ID)}
					if ci.Threshold != nil {
						t := a.cfg.Checks.Threshold(ci.ID, *ci.Threshold)
						r.Threshold = &t
						_, r.Configured = a.cfg.Checks.Thresholds[ci.ID]
					}
					rows = append(rows, r)
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			if format == "json" {
				return printJSON(stdout, rows)
			}
			var table [][]string
			for _, r := range rows {
				th := "-"
				if r.Threshold != nil {
					th = r.Threshold.String()
					if r.Configured {
						th += " (configured)"
					}
				}
				state := "on"
				if r.Disabled {
					state = "disabled"
				}
				table = append(table, []string{r.ID, r.Module, state, th, r.Description})
			}
			printTable(stdout, []string{"CHECK", "MODULE", "STATE", "THRESHOLD", "DESCRIPTION"}, table)
			return nil
		},
	}
	list.Flags().StringVarP(&format, "output", "o", "text", "output format: text, json")
	cmd.AddCommand(list)
	return cmd
}

func newConfigCmd(g *globalOptions, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or validate the configuration, or print an example",
	}
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the configuration file",
		Example: `  terminus config validate
  terminus config validate --config ./terminus.toml`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			a, err := newApp(g, stderr)
			if err != nil {
				return err
			}
			if a.cfg.Path == "" {
				if g.changed("config") {
					fmt.Fprintf(stdout, "no configuration file at %s: using the defaults\n", g.configPath)
				} else {
					fmt.Fprintf(stdout, "no configuration file in %s, %s or %s: using the defaults\n",
						config.DefaultPath, config.UserPath(), config.LocalPath)
				}
				return nil
			}
			mods, _ := a.reg.Enabled(a.sel)
			var names []string
			for _, m := range mods {
				names = append(names, m.Name())
			}
			fmt.Fprintf(stdout, "%s is valid; enabled modules: %s\n", a.cfg.Path, strings.Join(names, ", "))
			return nil
		},
	}
	example := &cobra.Command{
		Use:   "example",
		Short: "Print a commented terminus.toml with every module and threshold",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			a, err := newApp(g, stderr)
			if err != nil {
				return err
			}
			return writeExample(stdout, a.reg)
		},
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the layers considered and the merged configuration",
		Long: `Print the configuration files considered, weakest first, and the configuration merged from
the ones found. The output is itself a valid terminus.toml: the list of files is a comment.

The configuration is shown as read, without validating it: "terminus config validate" checks it.`,
		Example: `  terminus config show
  terminus config show --color never > merged.toml`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			paths := config.HierarchyPaths()
			if g.changed("config") {
				paths = []string{g.configPath}
			}
			m, err := config.Merge(paths)
			if err != nil {
				return err
			}
			if g.changed("config") && len(m.Loaded()) == 0 {
				return fmt.Errorf("open %s: %w", g.configPath, os.ErrNotExist)
			}
			o, err := renderOptions(g, stdout)
			if err != nil {
				return err
			}
			src, err := showConfig(m)
			if err != nil {
				return err
			}
			return output.HighlightTOML(stdout, src, o.Color)
		},
	}
	cmd.AddCommand(validate, example, show)
	return cmd
}

// showConfig lists the layers of m as comments, followed by the merged configuration.
func showConfig(m *config.Merged) (string, error) {
	var b strings.Builder
	b.WriteString("# Configuration files, weakest first:\n")
	width := 0
	for _, l := range m.Layers {
		width = max(width, len(displayPath(l.Path)))
	}
	for _, l := range m.Layers {
		state := "not found"
		if l.Found {
			state = "loaded"
		}
		fmt.Fprintf(&b, "#   %-*s  %s\n", width, displayPath(l.Path), state)
	}
	if len(m.Loaded()) == 0 {
		b.WriteString("#\n# No configuration file found: every default applies.\n")
		return b.String(), nil
	}
	src, err := m.TOML()
	if err != nil {
		return "", err
	}
	b.WriteString("\n" + src)
	return b.String(), nil
}

// displayPath makes relative paths (the per-run layer) absolute, so it is clear which file it is.
func displayPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// writeExample prints a configuration where every setting is commented out at its default.
func writeExample(w io.Writer, reg *module.Registry) error {
	var b strings.Builder
	b.WriteString(`# terminus configuration: /etc/terminus/terminus.toml
# Every setting is commented out at its default value.

# Maximum time for each module.
#timeout = "30s"

# Directory of the external modules (executables printing facts and findings as JSON).
#modules_dir = "/etc/terminus/modules.d"
`)
	for _, m := range reg.All() {
		if e, ok := m.(module.External); ok && e.External() {
			continue
		}
		fmt.Fprintf(&b, "\n# %s: %s\n#[modules.%s]\n", m.Name(), m.Description(), m.Name())
		if m.Core() {
			b.WriteString("#enabled = true\n")
		} else {
			b.WriteString("#enabled = false\n")
		}
		if c, ok := m.(module.Configurable); ok {
			for _, l := range strings.Split(c.ConfigExample(), "\n") {
				if l == "" {
					b.WriteString("#\n")
					continue
				}
				b.WriteString("#" + strings.TrimPrefix(l, "# ") + "\n")
			}
		}
	}
	b.WriteString("\n[checks]\n# Checks whose findings are dropped: IDs or prefixes (\"disk.*\").\n#disable = []\n")
	b.WriteString("\n# Findings dropped by subject: check ID (or prefix) -> glob patterns matched against Subject.\n#[checks.exclude]\n#\"unit.memory-limit\" = [\"*:app-*.service\"]\n")
	ids := reg.Checks()
	var keys []string
	for id, ci := range ids {
		if ci.Threshold != nil {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	for _, id := range keys {
		t := ids[id].Threshold
		fmt.Fprintf(&b, "\n# %s: %s (%s)\n#[checks.thresholds.%q]\n#warn = %v\n#fail = %v\n", id, ids[id].Description, t.String(), id, t.Warn, t.Fail)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
