package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/module"
)

func printTable(w io.Writer, header []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
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
		Short: "Validate the configuration or print an example",
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
				fmt.Fprintf(stdout, "no configuration file at %s: using the defaults\n", g.configPath)
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
	cmd.AddCommand(validate, example)
	return cmd
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
