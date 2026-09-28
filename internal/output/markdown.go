package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/enr/terminus/internal/model"
)

// Markdown renders a report for tickets, pull requests and wikis: summary, findings and modules
// as tables, facts as collapsible JSON blocks.
type Markdown struct{}

var severityLabel = map[model.Severity]string{
	model.SeverityOK:   "✔ ok",
	model.SeverityInfo: "ℹ info",
	model.SeverityWarn: "⚠ warn",
	model.SeverityFail: "✖ fail",
}

var mdCell = strings.NewReplacer("|", `\|`, "\n", " ", "\r", "", "<", "&lt;", ">", "&gt;", "&", "&amp;")

func cell(s string) string { return mdCell.Replace(s) }

// Render implements Renderer.
func (Markdown) Render(w io.Writer, r *model.Report, o Options) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "# terminus report: %s\n\n", cell(r.Meta.Hostname))
	fmt.Fprintf(b, "%s · terminus %s · %s\n\n", r.Meta.Timestamp.Format(time.RFC3339), orDash(r.Meta.Version), humanMillis(r.Meta.DurationMs))

	if o.Findings {
		s := r.Summary
		fmt.Fprintf(b, "**%d fail · %d warn · %d info · %d ok**\n\n", s.Fail, s.Warn, s.Info, s.OK)
		fs := shownFindings(r, o)
		b.WriteString("## Findings\n\n")
		if len(fs) == 0 {
			b.WriteString("No findings.\n\n")
		} else {
			b.WriteString("| Severity | Check | Subject | Message | Hint |\n|---|---|---|---|---|\n")
			for _, f := range fs {
				fmt.Fprintf(b, "| %s | `%s` | %s | %s | %s |\n", severityLabel[f.Severity], f.ID, cell(f.Subject), cell(f.Message), cell(f.Hint))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Modules\n\n| Module | Status | Duration | Notes |\n|---|---|---|---|\n")
	for _, n := range r.ModuleNames() {
		m := r.Modules[n]
		notes := m.SkipReason
		if len(m.Errors) > 0 {
			notes = strings.Join(m.Errors, "; ")
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", n, m.Status, humanMillis(m.DurationMs), cell(notes))
	}
	b.WriteString("\n")

	if o.Facts {
		b.WriteString("## Facts\n\n")
		for _, n := range r.ModuleNames() {
			m := r.Modules[n]
			if m.Facts == nil {
				continue
			}
			j, err := json.MarshalIndent(m.Facts, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintf(b, "<details>\n<summary>%s</summary>\n\n```json\n%s\n```\n\n</details>\n\n", n, strings.ReplaceAll(string(j), "```", "` ` `"))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func orDash(s string) string {
	if s == "" {
		return "dev"
	}
	return s
}
