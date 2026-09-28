package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/output"
	"github.com/enr/terminus/internal/remote"
	"github.com/enr/terminus/internal/runner"
)

func newRemoteCmd(g *globalOptions, stdout io.Writer) *cobra.Command {
	var (
		o         remote.Options
		format    string
		hostsFile string
		outputDir string
		verbose   bool
		problems  bool
	)
	cmd := &cobra.Command{
		Use:   "remote [flags] HOST... [-- TERMINUS FLAGS]",
		Short: "Run terminus on other machines through ssh",
		Long: `Run terminus on other machines through ssh and bring the reports back.

HOST is anything ssh accepts, with an optional port: srv-01, apps@web-02, web-03:2222.
~/.ssh/config, the agent and known_hosts apply as for ssh itself; ssh runs in batch mode.

terminus is copied to ~/.cache/terminus on each host (only when that version is not there
already); for another architecture put terminus-linux-<arch> in --binaries-dir or next to
terminus (see .sdlc/build-dist), or use a terminus installed there with --remote-binary.
--sudo requires --remote-binary: sudo must run a binary the ssh user cannot replace, not one
just copied into their own cache directory. The flags after "--" go to the remote terminus.

Exit code: the worst among the hosts; 3 when a host cannot be reached or fails.`,
		Example: `  terminus remote srv-01
  terminus remote --sudo --remote-binary /usr/local/bin/terminus srv-01 web-02 -- --only systemd,podman,quadlet
  terminus remote --hosts-file hosts.txt --output-dir reports/ -o markdown > all.md
  terminus diff reports-before/srv-01.json reports/srv-01.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, remoteArgs := args, []string(nil)
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				hosts, remoteArgs = args[:dash], args[dash:]
			}
			if hostsFile != "" {
				more, err := remote.ReadHostsFile(hostsFile)
				if err != nil {
					return err
				}
				hosts = append(hosts, more...)
			}
			if len(hosts) == 0 {
				return errors.New("no host given")
			}
			switch format {
			case "text", "markdown", "json":
			default:
				return fmt.Errorf("unknown output format %q (supported: text, markdown, json)", format)
			}
			o.Args = remoteArgs
			results := remote.Run(cmd.Context(), runner.Exec{}, hosts, o)

			if outputDir != "" {
				if err := saveReports(outputDir, results); err != nil {
					return err
				}
			}
			ro, err := renderOptions(g, stdout)
			if err != nil {
				return err
			}
			ro.Findings, ro.Verbose, ro.ProblemsOnly = true, verbose, problems
			if err := renderRemote(stdout, results, format, ro); err != nil {
				return err
			}
			code := model.ExitOK
			for _, r := range results {
				code = max(code, r.ExitCode())
			}
			if code != model.ExitOK {
				return &exitError{code: code}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&format, "output", "o", "text", "output format: text, markdown, json")
	f.StringVar(&hostsFile, "hosts-file", "", "read more hosts from this file, one per line (# comments)")
	f.StringVar(&outputDir, "output-dir", "", "save each JSON report in DIR/<host>.json")
	f.BoolVar(&o.Sudo, "sudo", false, "run the remote terminus with sudo -n (to inspect the managers and containers of every user); requires --remote-binary")
	f.StringVar(&o.RemoteBinary, "remote-binary", "", "use this terminus installed on the hosts instead of copying one")
	f.StringVar(&o.BinariesDir, "binaries-dir", "", "directory with terminus-linux-<arch> for other architectures")
	f.StringVar(&o.RemoteConfig, "remote-config", "", "copy this terminus.toml to the hosts and use it")
	f.StringVar(&o.SSH, "ssh", "ssh", "ssh client")
	f.StringArrayVar(&o.SSHOptions, "ssh-option", nil, "ssh option, as for ssh -o (repeatable)")
	f.IntVar(&o.Parallel, "parallel", 4, "hosts processed at the same time")
	f.DurationVar(&o.Timeout, "host-timeout", 5*time.Minute, "maximum time for each host")
	f.BoolVarP(&verbose, "verbose", "v", false, "show evidence and hints of the findings")
	f.BoolVar(&problems, "problems", false, "show only warn and fail findings")
	return cmd
}

// fileName turns a host into a file name: apps@web:2222 → apps_web_2222.
func fileName(host string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			return r
		}
		return '_'
	}, host)
}

func saveReports(dir string, results []remote.Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, r := range results {
		if r.Report == nil {
			continue
		}
		path := filepath.Join(dir, fileName(r.Host)+".json")
		err := writeOutput(path, nil, func(w io.Writer) error { return (output.JSON{}).Render(w, r.Report, output.Options{}) })
		if err != nil {
			return err
		}
	}
	return nil
}

func renderRemote(w io.Writer, results []remote.Result, format string, o output.Options) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"hosts": results})
	}
	md := format == "markdown"
	if md {
		fmt.Fprint(w, "# terminus remote\n\n| Host | Result | Fail | Warn | Arch | Copied |\n|---|---|---|---|---|---|\n")
	}
	var table [][]string
	for _, r := range results {
		status, fail, warn := "ok", "", ""
		switch {
		case r.Error != "":
			status = "error: " + r.Error
		default:
			fail, warn = fmt.Sprint(r.Report.Summary.Fail), fmt.Sprint(r.Report.Summary.Warn)
			status = map[int]string{model.ExitOK: "ok", model.ExitWarn: "warn", model.ExitFail: "fail"}[r.ExitCode()]
		}
		copied := map[bool]string{true: "yes", false: "no"}[r.Copied]
		if md {
			fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", r.Host, strings.ReplaceAll(status, "|", `\|`), fail, warn, r.Arch, copied)
		} else {
			table = append(table, []string{r.Host, status, fail, warn, r.Arch, copied})
		}
	}
	if md {
		fmt.Fprintln(w)
	}
	for _, r := range results {
		if r.Report == nil {
			continue
		}
		if md {
			if err := (output.Markdown{}).Render(w, r.Report, o); err != nil {
				return err
			}
			continue
		}
		fmt.Fprintf(w, "━━ %s ━━\n", r.Host)
		if err := (output.Text{}).Render(w, r.Report, o); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}
	if !md {
		printTable(w, []string{"HOST", "RESULT", "FAIL", "WARN", "ARCH", "COPIED"}, table)
	}
	return nil
}
