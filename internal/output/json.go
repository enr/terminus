package output

import (
	"encoding/json"
	"io"

	"github.com/enr/terminus/internal/model"
)

// JSON renders the complete report as indented JSON. It is the machine-readable format and the
// input of the commands that work on saved reports.
type JSON struct{}

// Render implements Renderer.
func (JSON) Render(w io.Writer, r *model.Report, _ Options) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
