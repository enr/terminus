// Package output renders reports. The report model holds raw values only (bytes, milliseconds,
// UTC timestamps); making them readable for humans is the job of the renderers.
package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/enr/terminus/internal/model"
)

// Options tune the rendering.
type Options struct {
	// Findings shows the summary and the findings; off when no check ran (text renderer).
	Findings bool
	// Color enables ANSI colors (text renderer).
	Color bool
	// Verbose adds evidence and hints to findings (text renderer).
	Verbose bool
	// ProblemsOnly hides the findings that are neither warn nor fail (text renderer).
	ProblemsOnly bool
	// Facts adds the collected facts to the output (text renderer).
	Facts bool
}

// Renderer writes a report in a format.
type Renderer interface {
	Render(w io.Writer, r *model.Report, o Options) error
}

// Formats lists the supported output formats.
var Formats = []string{"text", "json"}

// ForFormat returns the renderer of a format.
func ForFormat(format string) (Renderer, error) {
	switch format {
	case "text", "":
		return Text{}, nil
	case "json":
		return JSON{}, nil
	default:
		return nil, fmt.Errorf("unknown output format %q (supported: %s)", format, strings.Join(Formats, ", "))
	}
}

// ColorMode is the value of the --color flag.
type ColorMode string

// Color modes.
const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

// UseColor decides whether to use colors on f: "auto" means only on a terminal and only when
// NO_COLOR (https://no-color.org) is not set.
func UseColor(mode ColorMode, f *os.File) (bool, error) {
	switch mode {
	case ColorAlways:
		return true, nil
	case ColorNever:
		return false, nil
	case ColorAuto, "":
		if os.Getenv("NO_COLOR") != "" {
			return false, nil
		}
		return term.IsTerminal(int(f.Fd())), nil
	default:
		return false, fmt.Errorf("invalid color mode %q (auto, always, never)", mode)
	}
}
