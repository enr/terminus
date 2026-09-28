package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/output"
	"github.com/enr/terminus/internal/probe"
	"github.com/enr/terminus/internal/runner"
)

func newProbeCmd(g *globalOptions, stdout io.Writer) *cobra.Command {
	var (
		t       probe.Target
		co      checkOptions
		yes     bool
		quiet   bool
		prober  = &probe.Prober{Runner: runner.Exec{}}
		signals = []os.Signal{os.Interrupt, syscall.SIGTERM}
	)
	cmd := &cobra.Command{
		Use:   "probe --unit UNIT | --container NAME [flags]",
		Short: "Stop a service on purpose to verify that the monitoring notices (changes the machine)",
		Long: `Stop a service for a while to verify that its health checks and the external monitoring
really notice the outage, then start it again.

This is the only terminus command that changes the machine. It asks for confirmation (--yes
to skip it; without a terminal --yes is required). The service is started again in every case:
when the probe fails, when the stop fails, and on Ctrl-C or SIGTERM.

While the service is down:
  --http       the endpoint must stop answering (or answer 5xx)
  --check-cmd  a shell command that exits 0 when the monitoring noticed the outage
               (e.g. it finds the alert in the easeprobe log or in the alertmanager API)
Afterwards the unit must be active, the container healthy and the endpoint answering again
within --recover.

--unit stops a systemd unit (with --user, a unit of that user's manager; needs root for
another user); --container alone stops a podman container, with --unit it is only watched.`,
		Example: `  terminus probe --unit myapp.service --user apps --container myapp \
      --http https://app.example.org/health \
      --check-cmd 'grep -q "myapp.*DOWN" /var/log/easeprobe.log' --wait 60s
  terminus probe --container web --http http://127.0.0.1:8080/ --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := t.Validate(); err != nil {
				return err
			}
			renderer, err := output.ForFormat(co.output)
			if err != nil {
				return err
			}
			if !yes {
				if err := confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), t); err != nil {
					return err
				}
			}
			// The probe context ends on the first signal; the start of the service then runs on
			// its own context, so a second signal does not stop it either.
			ctx, stop := signal.NotifyContext(cmd.Context(), signals...)
			defer stop()
			stderr := cmd.ErrOrStderr()
			if !quiet {
				prober.Log = func(s string) { fmt.Fprintf(stderr, "probe: %s\n", s) }
			}
			r := prober.Run(ctx, t)

			o, err := renderOptions(g, stdout)
			if err != nil {
				return err
			}
			if co.outputFile != "" {
				o.Color = false
			}
			o.Findings, o.Verbose, o.ProblemsOnly, o.Facts = true, co.verbose, co.problems, co.facts
			if err := writeOutput(co.outputFile, stdout, func(w io.Writer) error { return renderer.Render(w, r, o) }); err != nil {
				return err
			}
			if r.Modules[probe.Name].Status == model.StatusError {
				return &exitError{code: model.ExitError, err: errors.New(strings.Join(r.Modules[probe.Name].Errors, "; "))}
			}
			if code := r.ExitCode(); code != model.ExitOK {
				return &exitError{code: code}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&t.Unit, "unit", "", "systemd unit to stop")
	f.StringVar(&t.User, "user", "", "the unit or the container belongs to this user (systemctl --user, rootless podman)")
	f.StringVar(&t.Container, "container", "", "podman container: stopped when there is no --unit, otherwise watched")
	f.StringVar(&t.HTTP, "http", "", "URL that must stop answering while the service is down")
	f.StringVar(&t.CheckCmd, "check-cmd", "", "shell command that exits 0 when the monitoring noticed the outage")
	f.DurationVar(&t.Wait, "wait", 15*time.Second, "how long the service stays down")
	f.DurationVar(&t.Recover, "recover", 60*time.Second, "maximum wait for the service to be back")
	f.BoolVar(&yes, "yes", false, "do not ask for confirmation")
	f.BoolVarP(&quiet, "quiet", "q", false, "do not print the progress on stderr")
	addCheckFlags(cmd, &co, "text")
	f.BoolVar(&co.facts, "facts", false, "include the observations in the output")
	return cmd
}

// confirm asks before stopping; without a terminal it refuses, --yes is needed.
func confirm(in io.Reader, out io.Writer, t probe.Target) error {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return errors.New("probe stops " + t.Describe() + ": confirm with --yes when not running on a terminal")
	}
	fmt.Fprintf(out, "This stops %s for %s, then starts it again. Continue? [y/N] ", t.Describe(), t.Wait)
	answer, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes", "s", "si", "sì":
		return nil
	}
	return &exitError{code: model.ExitError, err: errors.New("canceled")}
}
