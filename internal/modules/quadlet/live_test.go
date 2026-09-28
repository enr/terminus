package quadlet

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

// TestLive runs against the quadlet files of the machine when TERMINUS_LIVE_QUADLET is set.
func TestLive(t *testing.T) {
	if os.Getenv("TERMINUS_LIVE_QUADLET") == "" {
		t.Skip("set TERMINUS_LIVE_QUADLET=1 to run against the local quadlet files")
	}
	m := New()
	m.users = users.Spec{Names: []string{}}
	got, err := m.Collect(context.Background(), &module.Env{Runner: runner.Exec{}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(got, "", "  ")
	t.Log(string(b))
	for _, f := range m.Check(nil, got) {
		t.Logf("%-5s %-26s %-28s %s | %s | %v", f.Severity, f.ID, f.Subject, f.Message, f.Hint, f.Evidence)
	}
}
