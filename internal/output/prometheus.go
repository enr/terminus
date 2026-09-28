package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/enr/terminus/internal/model"
)

// Prometheus renders the report in the text exposition format, for the textfile collector of
// node_exporter: alerts on terminus findings without another agent.
type Prometheus struct{}

var promEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func label(name, value string) string { return name + `="` + promEscaper.Replace(value) + `"` }

// Render implements Renderer.
func (Prometheus) Render(w io.Writer, r *model.Report, o Options) error {
	b := &strings.Builder{}
	metric := func(name, help, typ string) {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}

	metric("terminus_finding_severity", "Severity of each finding: 0 ok, 1 info, 2 warn, 3 fail.", "gauge")
	for _, f := range shownFindings(r, o) {
		fmt.Fprintf(b, "terminus_finding_severity{%s,%s,%s} %d\n",
			label("id", f.ID), label("module", f.Module), label("subject", f.Subject), int(f.Severity))
	}

	metric("terminus_findings", "Number of findings by severity.", "gauge")
	for _, s := range []struct {
		name string
		n    int
	}{{"ok", r.Summary.OK}, {"info", r.Summary.Info}, {"warn", r.Summary.Warn}, {"fail", r.Summary.Fail}} {
		fmt.Fprintf(b, "terminus_findings{%s} %d\n", label("severity", s.name), s.n)
	}

	metric("terminus_module_up", "1 when the module collected its facts (ok or partial), 0 otherwise.", "gauge")
	for _, n := range r.ModuleNames() {
		m := r.Modules[n]
		up := 0
		if m.Status == model.StatusOK || m.Status == model.StatusPartial {
			up = 1
		}
		fmt.Fprintf(b, "terminus_module_up{%s,%s} %d\n", label("module", n), label("status", string(m.Status)), up)
	}
	metric("terminus_module_duration_seconds", "Time spent collecting the facts of the module.", "gauge")
	for _, n := range r.ModuleNames() {
		fmt.Fprintf(b, "terminus_module_duration_seconds{%s} %g\n", label("module", n), float64(r.Modules[n].DurationMs)/1000)
	}

	metric("terminus_exit_code", "Exit code of the run: 0 ok, 1 warn, 2 fail.", "gauge")
	fmt.Fprintf(b, "terminus_exit_code %d\n", r.ExitCode())
	metric("terminus_last_run_timestamp_seconds", "Time of the run.", "gauge")
	fmt.Fprintf(b, "terminus_last_run_timestamp_seconds %d\n", r.Meta.Timestamp.Unix())
	metric("terminus_run_duration_seconds", "Duration of the run.", "gauge")
	fmt.Fprintf(b, "terminus_run_duration_seconds %g\n", float64(r.Meta.DurationMs)/1000)

	_, err := io.WriteString(w, b.String())
	return err
}
