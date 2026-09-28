package output

import (
	"regexp"
	"strings"
	"testing"
)

func TestHighlightTOML(t *testing.T) {
	src := `# layers
timeout = "30s"

[checks.thresholds."disk.usage"]
warn = 0.8
"a = b" = ['x#y', "q\"#"] # note
p = { enabled = true, n = 1979-05-27T07:32:00Z }
`
	var plain strings.Builder
	if err := HighlightTOML(&plain, src, false); err != nil {
		t.Fatal(err)
	}
	if plain.String() != src {
		t.Errorf("without color the source must be unchanged:\n%s", plain.String())
	}

	var colored strings.Builder
	if err := HighlightTOML(&colored, src, true); err != nil {
		t.Fatal(err)
	}
	out := colored.String()
	if got := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(out, ""); got != src {
		t.Errorf("highlighting changed the text:\n%s", got)
	}
	s := newTOMLStyles(&colored, true)
	for _, want := range []string{
		s.comment.Render("# layers"),
		s.key.Render("timeout") + " = " + s.str.Render(`"30s"`),
		s.header.Render(`[checks.thresholds."disk.usage"]`),
		s.num.Render("0.8"),
		// Quotes hide "=" and "#".
		s.key.Render(`"a = b"`) + " = [" + s.str.Render(`'x#y'`) + ", " + s.str.Render(`"q\"#"`) + "] " + s.comment.Render("# note"),
		"{ " + s.key.Render("enabled") + " = " + s.num.Render("true"),
		s.num.Render("1979-05-27T07:32:00Z"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%q", want, out)
		}
	}
}
