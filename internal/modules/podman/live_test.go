package podman

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
	"github.com/enr/terminus/internal/users"
)

// TestLive runs against the podman of the machine when TERMINUS_LIVE_PODMAN is set.
func TestLive(t *testing.T) {
	if os.Getenv("TERMINUS_LIVE_PODMAN") == "" {
		t.Skip("set TERMINUS_LIVE_PODMAN=1 to run against the local podman")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
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
		t.Logf("%-5s %-22s %-20s %s", f.Severity, f.ID, f.Subject, f.Message)
	}
}
