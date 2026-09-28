package diff

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/enr/terminus/internal/model"
)

// Render writes a comparison as text, json or markdown.
func Render(w io.Writer, r Result, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "markdown", "md":
		return markdown(w, r)
	case "text", "":
		return text(w, r)
	default:
		return fmt.Errorf("unknown output format %q (supported: text, json, markdown)", format)
	}
}

func sev(f *model.Finding) string { return f.Severity.String() }

func describe(c FindingChange) (mark, what, msg string) {
	switch c.Kind {
	case New:
		return "+", "new " + sev(c.After), c.After.Message
	case Resolved:
		return "-", "resolved (was " + sev(c.Before) + ")", c.Before.Message
	default:
		mark = "~"
		if c.Worse() {
			mark = "!"
		}
		return mark, sev(c.Before) + " → " + sev(c.After), c.After.Message
	}
}

func subject(c FindingChange) string {
	if c.Subject == "" {
		return ""
	}
	return " [" + c.Subject + "]"
}

func text(w io.Writer, r Result) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "before: %s %s\nafter:  %s %s\n", r.Before.Hostname, r.Before.Timestamp, r.After.Hostname, r.After.Timestamp)
	if r.Before.Hostname != r.After.Hostname {
		b.WriteString("warning: the reports come from different hosts\n")
	}
	fmt.Fprintf(b, "\nFindings: %d changes\n", len(r.Findings))
	for _, c := range r.Findings {
		mark, what, msg := describe(c)
		fmt.Fprintf(b, "  %s %-26s %s%s: %s\n", mark, what, c.ID, subject(c), msg)
	}
	if r.Facts != nil {
		fmt.Fprintf(b, "\nFacts: %d changes\n", len(r.Facts))
		for _, c := range r.Facts {
			switch c.Kind {
			case Added:
				fmt.Fprintf(b, "  + %s = %v\n", c.Path, c.After)
			case Removed:
				fmt.Fprintf(b, "  - %s (was %v)\n", c.Path, c.Before)
			default:
				fmt.Fprintf(b, "  ~ %s: %v → %v\n", c.Path, c.Before, c.After)
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

var mdCell = strings.NewReplacer("|", `\|`, "\n", " ", "<", "&lt;", ">", "&gt;", "&", "&amp;")

func markdown(w io.Writer, r Result) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "# terminus diff: %s\n\n%s → %s\n\n## Findings\n\n", mdCell.Replace(r.After.Hostname), r.Before.Timestamp, r.After.Timestamp)
	if len(r.Findings) == 0 {
		b.WriteString("No changes.\n\n")
	} else {
		b.WriteString("| | Change | Check | Subject | Message |\n|---|---|---|---|---|\n")
		for _, c := range r.Findings {
			mark, what, msg := describe(c)
			fmt.Fprintf(b, "| %s | %s | `%s` | %s | %s |\n", mark, what, c.ID, mdCell.Replace(c.Subject), mdCell.Replace(msg))
		}
		b.WriteString("\n")
	}
	if r.Facts != nil {
		b.WriteString("## Facts\n\n| | Path | Before | After |\n|---|---|---|---|\n")
		for _, c := range r.Facts {
			mark := map[string]string{Added: "+", Removed: "-", Changed: "~"}[c.Kind]
			fmt.Fprintf(b, "| %s | `%s` | %s | %s |\n", mark, c.Path, mdCell.Replace(str(c.Before)), mdCell.Replace(str(c.After)))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
