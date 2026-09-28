package output

import (
	"encoding/json"
	"io"
	"time"

	"github.com/enr/terminus/internal/model"
)

// JSONL renders one JSON object per finding, with the host and the time of the run: the format of
// log shippers (Loki, Vector, Fluent Bit).
type JSONL struct{}

type jsonlLine struct {
	Timestamp time.Time      `json:"timestamp"`
	Host      string         `json:"host"`
	Module    string         `json:"module"`
	ID        string         `json:"id"`
	Severity  model.Severity `json:"severity"`
	Subject   string         `json:"subject,omitempty"`
	Message   string         `json:"message"`
	Hint      string         `json:"hint,omitempty"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

// Render implements Renderer.
func (JSONL) Render(w io.Writer, r *model.Report, o Options) error {
	enc := json.NewEncoder(w)
	for _, f := range shownFindings(r, o) {
		if err := enc.Encode(jsonlLine{
			Timestamp: r.Meta.Timestamp, Host: r.Meta.Hostname, Module: f.Module, ID: f.ID,
			Severity: f.Severity, Subject: f.Subject, Message: f.Message, Hint: f.Hint, Evidence: f.Evidence,
		}); err != nil {
			return err
		}
	}
	return nil
}
