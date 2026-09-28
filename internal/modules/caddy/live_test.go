package caddy

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

// TestLive runs against the local Caddy when TERMINUS_LIVE_CADDY is set.
func TestLive(t *testing.T) {
	if os.Getenv("TERMINUS_LIVE_CADDY") == "" {
		t.Skip("set TERMINUS_LIVE_CADDY=1 to run against the local Caddy")
	}
	m := New()
	got, err := m.Collect(context.Background(), &module.Env{Runner: runner.Exec{}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(got, "", "  ")
	t.Log(string(b))
	for _, f := range m.Check(nil, got) {
		t.Logf("%-5s %-17s %-20s %s", f.Severity, f.ID, f.Subject, f.Message)
	}
}
