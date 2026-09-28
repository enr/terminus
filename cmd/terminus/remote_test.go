package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
)

// fakeSSH writes an ssh client that runs the command locally with HOME in a temporary directory;
// the host "down" is unreachable.
func fakeSSH(t *testing.T, dir string) string {
	t.Helper()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
host=$1
shift
if [ "$host" = down ]; then echo "ssh: connect to host down port 22: Connection refused" >&2; exit 255; fi
HOME=` + home + ` exec sh -c "$1"
`
	p := filepath.Join(dir, "ssh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRemoteCommand(t *testing.T) {
	dir := t.TempDir()
	ssh := fakeSSH(t, dir)

	// The "remote terminus" prints a report with a warning, and its arguments on stderr.
	r := model.NewReport()
	r.Meta.Hostname = "srv-01"
	r.AddFindings(model.Finding{ID: "disk.usage", Module: "storage", Severity: model.SeverityWarn, Message: "88% used"})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "terminus")
	script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "args") + "\ncat " + filepath.Join(dir, "report.json") + "\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "reports")
	code, stdout, stderr := runCLI(t, "remote", "--ssh", ssh, "--remote-binary", bin, "--output-dir", out,
		"--color", "never", "apps@srv-01:2222", "down", "--", "--only", "storage")
	if code != model.ExitError {
		t.Fatalf("exit code = %d, want 3 (a host is down); stderr: %s", code, stderr)
	}
	for _, want := range []string{"━━ apps@srv-01:2222 ━━", "disk.usage", "Connection refused", "HOST"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if got := strings.TrimSpace(string(args)); got != "report -o json --color never --only storage" {
		t.Errorf("remote arguments = %q", got)
	}
	saved, err := os.ReadFile(filepath.Join(out, "apps_srv-01_2222.json"))
	if err != nil || !strings.Contains(string(saved), "disk.usage") {
		t.Errorf("saved report: %v %s", err, saved)
	}
	if _, err := os.Stat(filepath.Join(out, "down.json")); err == nil {
		t.Error("a report was saved for the unreachable host")
	}

	// Only the reachable host: the exit code is the one of its report.
	code, stdout, _ = runCLI(t, "remote", "--ssh", ssh, "--remote-binary", bin, "-o", "json", "srv-01")
	if code != model.ExitWarn {
		t.Errorf("exit code = %d, want 1", code)
	}
	var doc struct {
		Hosts []struct {
			Host   string
			Report *model.Report
		}
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Hosts) != 1 || doc.Hosts[0].Report == nil {
		t.Fatalf("json output: %v\n%s", err, stdout)
	}
}

func TestProbeRefusesWithoutConfirmation(t *testing.T) {
	code, _, stderr := runCLI(t, "probe", "--container", "web")
	if code != model.ExitError || !strings.Contains(stderr, "--yes") {
		t.Errorf("exit code %d, stderr %q", code, stderr)
	}
	code, _, stderr = runCLI(t, "probe", "--yes")
	if code != model.ExitError || !strings.Contains(stderr, "--unit or --container") {
		t.Errorf("exit code %d, stderr %q", code, stderr)
	}
}
