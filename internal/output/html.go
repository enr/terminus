package output

import (
	"encoding/json"
	"html/template"
	"io"
	"time"

	"github.com/enr/terminus/internal/model"
)

// HTML renders a self-contained page (inline CSS, no script, no external resource) with the
// same content as the Markdown report: a file to archive or attach.
type HTML struct{}

type htmlModule struct {
	Name     string
	Status   string
	Duration string
	Notes    []string
	Facts    string
}

type htmlData struct {
	Report   *model.Report
	Time     string
	Version  string
	Duration string
	Findings []model.Finding
	Show     bool
	Modules  []htmlModule
	Facts    bool
}

var htmlTmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"sev":   func(s model.Severity) string { return s.String() },
	"label": func(s model.Severity) string { return severityLabel[s] },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>terminus report: {{.Report.Meta.Hostname}}</title>
<style>
:root { --bg: #fff; --fg: #1f2328; --muted: #656d76; --line: #d0d7de; --head: #f6f8fa;
  --ok: #1a7f37; --info: #0969da; --warn: #9a6700; --fail: #cf222e; }
@media (prefers-color-scheme: dark) {
  :root { --bg: #0d1117; --fg: #e6edf3; --muted: #8d96a0; --line: #30363d; --head: #161b22;
    --ok: #3fb950; --info: #58a6ff; --warn: #d29922; --fail: #f85149; }
}
body { background: var(--bg); color: var(--fg); font: 15px/1.5 system-ui, sans-serif; margin: 0; padding: 24px 16px; }
main { max-width: 1100px; margin: 0 auto; }
h1 { font-size: 1.6em; margin: 0 0 4px; } h2 { font-size: 1.2em; margin-top: 32px; }
.meta { color: var(--muted); margin-bottom: 16px; }
.summary span { display: inline-block; margin-right: 16px; font-weight: 600; }
table { border-collapse: collapse; width: 100%; display: block; overflow-x: auto; }
th, td { border: 1px solid var(--line); padding: 6px 10px; text-align: left; vertical-align: top; }
th { background: var(--head); }
code, pre { font: 13px/1.4 ui-monospace, monospace; }
pre { background: var(--head); padding: 12px; overflow-x: auto; border-radius: 6px; }
.ok { color: var(--ok); } .info { color: var(--info); } .warn { color: var(--warn); } .fail { color: var(--fail); }
.sev, td code { white-space: nowrap; } .sev { font-weight: 600; }
.hint { color: var(--muted); }
details { margin: 8px 0; } summary { cursor: pointer; font-weight: 600; }
</style>
</head>
<body>
<main>
<h1>terminus report: {{.Report.Meta.Hostname}}</h1>
<div class="meta">{{.Time}} · terminus {{.Version}} · {{.Duration}}</div>
{{- if .Show}}
<div class="summary">
<span class="fail">✖ {{.Report.Summary.Fail}} fail</span><span class="warn">⚠ {{.Report.Summary.Warn}} warn</span><span class="info">ℹ {{.Report.Summary.Info}} info</span><span class="ok">✔ {{.Report.Summary.OK}} ok</span>
</div>
<h2>Findings</h2>
{{- if .Findings}}
<table>
<tr><th>Severity</th><th>Check</th><th>Subject</th><th>Message</th></tr>
{{- range .Findings}}
<tr><td class="sev {{sev .Severity}}">{{label .Severity}}</td><td><code>{{.ID}}</code></td><td>{{.Subject}}</td><td>{{.Message}}{{if .Hint}}<br><span class="hint">{{.Hint}}</span>{{end}}</td></tr>
{{- end}}
</table>
{{- else}}
<p>No findings.</p>
{{- end}}
{{- end}}
<h2>Modules</h2>
<table>
<tr><th>Module</th><th>Status</th><th>Duration</th><th>Notes</th></tr>
{{- range .Modules}}
<tr><td>{{.Name}}</td><td>{{.Status}}</td><td>{{.Duration}}</td><td>{{range .Notes}}{{.}}<br>{{end}}</td></tr>
{{- end}}
</table>
{{- if .Facts}}
<h2>Facts</h2>
{{- range .Modules}}{{if .Facts}}
<details><summary>{{.Name}}</summary><pre>{{.Facts}}</pre></details>
{{- end}}{{end}}
{{- end}}
</main>
</body>
</html>
`))

// Render implements Renderer.
func (HTML) Render(w io.Writer, r *model.Report, o Options) error {
	d := htmlData{
		Report:   r,
		Time:     r.Meta.Timestamp.Format(time.RFC3339),
		Version:  orDash(r.Meta.Version),
		Duration: humanMillis(r.Meta.DurationMs),
		Findings: shownFindings(r, o),
		Show:     o.Findings,
		Facts:    o.Facts,
	}
	for _, n := range r.ModuleNames() {
		m := r.Modules[n]
		hm := htmlModule{Name: n, Status: string(m.Status), Duration: humanMillis(m.DurationMs), Notes: m.Errors}
		if m.SkipReason != "" {
			hm.Notes = append(hm.Notes, m.SkipReason)
		}
		if o.Facts && m.Facts != nil {
			j, err := json.MarshalIndent(m.Facts, "", "  ")
			if err != nil {
				return err
			}
			hm.Facts = string(j)
		}
		d.Modules = append(d.Modules, hm)
	}
	return htmlTmpl.Execute(w, d)
}
