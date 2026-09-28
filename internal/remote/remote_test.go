package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/runner"
)

const probeCmd = `uname -m && printf '%s\n' "$HOME"`

func sshKey(dest, port, command string) string {
	args := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	if port != "" {
		args = append(args, "-p", port)
	}
	return strings.Join(append(args, "--", dest, command), " ")
}

func reportJSON(t *testing.T, sev model.Severity) []byte {
	r := model.NewReport()
	r.Meta.Hostname = "srv"
	r.AddFindings(model.Finding{ID: "x.y", Severity: sev, Message: "m"})
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func setup(t *testing.T) (Options, string, string) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "terminus")
	os.WriteFile(exe, []byte("amd64 binary"), 0o755)
	os.WriteFile(filepath.Join(dir, "terminus-linux-arm64"), []byte("arm64 binary"), 0o755)
	sum, _ := fileHash(exe)
	return Options{Executable: func() (string, error) { return exe, nil }, Arch: "amd64"}, dir, sum
}

func TestRunHosts(t *testing.T) {
	o, dir, sum := setup(t)
	armSum, _ := fileHash(filepath.Join(dir, "terminus-linux-arm64"))
	bin := "/home/apps/.cache/terminus/terminus-" + sum[:16]
	armBin := "/root/.cache/terminus/terminus-" + armSum[:16]
	r := &runner.Fake{Results: map[string]runner.Result{
		// srv-01: amd64, binary already cached, warnings.
		sshKey("apps@srv-01", "", probeCmd):                                           {Stdout: []byte("x86_64\n/home/apps\n")},
		sshKey("apps@srv-01", "", "sha256sum "+bin+" 2>/dev/null"):                    {Stdout: []byte(sum + "  " + bin + "\n")},
		sshKey("apps@srv-01", "", bin+" report -o json --color never --only systemd"): {Stdout: reportJSON(t, model.SeverityWarn), ExitCode: 1},
		// arm: arm64 on port 2222, binary copied, failures.
		sshKey("arm", "2222", probeCmd):                           {Stdout: []byte("aarch64\n/root\n")},
		sshKey("arm", "2222", "sha256sum "+armBin+" 2>/dev/null"): {ExitCode: 1},
		sshKey("arm", "2222", "mkdir -p /root/.cache/terminus && cat > "+armBin+".tmp && chmod 755 "+armBin+".tmp && mv "+armBin+".tmp "+armBin): {},
		sshKey("arm", "2222", armBin+" report -o json --color never --only systemd"):                                                             {Stdout: reportJSON(t, model.SeverityFail), ExitCode: 2},
		// weird: unsupported architecture.
		sshKey("weird", "", probeCmd): {Stdout: []byte("riscv64\n/root\n")},
		// down: ssh fails.
		sshKey("down", "", probeCmd): {ExitCode: 255, Stderr: []byte("ssh: connect to host down port 22: Connection refused")},
	}}
	// --sudo is not used here: it requires --remote-binary (see TestRemoteSudoRequiresRemoteBinary),
	// since it would otherwise let sudo -n run a binary the ssh user just copied into their own
	// cache directory.
	o.Args = []string{"--only", "systemd"}
	res := Run(context.Background(), r, []string{"apps@srv-01", "arm:2222", "weird", "down"}, o)

	if res[0].Error != "" || res[0].Copied || res[0].ExitCode() != model.ExitWarn || res[0].Arch != "x86_64" {
		t.Errorf("srv-01: %+v", res[0])
	}
	if res[1].Error != "" || !res[1].Copied || res[1].ExitCode() != model.ExitFail {
		t.Errorf("arm: %+v", res[1])
	}
	if !strings.Contains(res[2].Error, `unsupported remote architecture "riscv64"`) || res[2].ExitCode() != model.ExitError {
		t.Errorf("weird: %+v", res[2])
	}
	if !strings.Contains(res[3].Error, "Connection refused") {
		t.Errorf("down: %+v", res[3])
	}
	uploaded := false
	for _, c := range r.Calls {
		if strings.Contains(runner.Key(c), "cat >") {
			uploaded = c.Stdin != nil
		}
		if c.Args[len(c.Args)-2] != "apps@srv-01" && c.Args[len(c.Args)-2] != "arm" && c.Args[len(c.Args)-2] != "weird" && c.Args[len(c.Args)-2] != "down" {
			t.Errorf("host not before the command: %v", c.Args)
		}
	}
	if !uploaded {
		t.Error("upload without stdin")
	}
}

func TestRemoteBinaryConfigAndErrors(t *testing.T) {
	o, dir, _ := setup(t)
	cfg := filepath.Join(dir, "terminus.toml")
	os.WriteFile(cfg, []byte("timeout = \"10s\"\n"), 0o644)
	csum, _ := fileHash(cfg)
	cfgPath := "/root/.cache/terminus/config-" + csum[:16] + ".toml"
	r := &runner.Fake{Results: map[string]runner.Result{
		sshKey("h", "", probeCmd):                            {Stdout: []byte("x86_64\n/root\n")},
		sshKey("h", "", "sha256sum "+cfgPath+" 2>/dev/null"): {Stdout: []byte(csum + "  x\n")},
		sshKey("h", "", "/usr/local/bin/terminus report -o json --color never --config "+cfgPath+" --users 'a b'"): {Stdout: []byte("not json"), Stderr: []byte("oops")},
	}}
	o.RemoteBinary, o.RemoteConfig, o.Args = "/usr/local/bin/terminus", cfg, []string{"--users", "a b"}
	res := Run(context.Background(), r, []string{"h"}, o)
	if !strings.Contains(res[0].Error, "invalid report") || !strings.Contains(res[0].Error, "oops") {
		t.Errorf("result: %+v", res[0])
	}

	r.Results[sshKey("h", "", "/usr/local/bin/terminus report -o json --color never --config "+cfgPath+" --users 'a b'")] = runner.Result{ExitCode: 3, Stderr: []byte("invalid configuration")}
	if res := Run(context.Background(), r, []string{"h"}, o); !strings.Contains(res[0].Error, "exit code 3: invalid configuration") {
		t.Errorf("remote error: %+v", res[0])
	}

	o2, _, _ := setup(t)
	o2.Executable = func() (string, error) { return "", errors.New("no exe") }
	r2 := &runner.Fake{Results: map[string]runner.Result{sshKey("h", "", probeCmd): {Stdout: []byte("x86_64\n/root\n")}}}
	if res := Run(context.Background(), r2, []string{"h"}, o2); !strings.Contains(res[0].Error, "no exe") {
		t.Errorf("no executable: %+v", res[0])
	}
	o3, _, _ := setup(t)
	o3.Arch = "arm64" // local arm64: an amd64 host needs terminus-linux-amd64
	if res := Run(context.Background(), r2, []string{"h"}, o3); !strings.Contains(res[0].Error, "terminus-linux-amd64") {
		t.Errorf("missing binary: %+v", res[0])
	}
}

func TestRemoteSudoRequiresRemoteBinary(t *testing.T) {
	// --sudo copies nothing when --remote-binary is set: sudo -n only ever runs a binary the
	// ssh user does not control.
	o, _, _ := setup(t)
	r := &runner.Fake{Results: map[string]runner.Result{
		sshKey("h", "", probeCmd): {Stdout: []byte("x86_64\n/root\n")},
		sshKey("h", "", "sudo -n /usr/local/bin/terminus report -o json --color never"): {Stdout: reportJSON(t, model.SeverityOK)},
	}}
	o.Sudo, o.RemoteBinary = true, "/usr/local/bin/terminus"
	if res := Run(context.Background(), r, []string{"h"}, o); res[0].Error != "" {
		t.Errorf("sudo with --remote-binary: %+v", res[0])
	}

	// Without --remote-binary, --sudo would run sudo -n on a binary just copied into the ssh
	// user's own (writable) cache directory: refuse before even connecting.
	o2, _, _ := setup(t)
	r2 := &runner.Fake{}
	o2.Sudo = true
	res := Run(context.Background(), r2, []string{"h"}, o2)
	if !strings.Contains(res[0].Error, "--sudo requires --remote-binary") {
		t.Errorf("error = %q, want the --remote-binary requirement", res[0].Error)
	}
	if len(r2.Calls) != 0 {
		t.Errorf("ssh was called before the check: %v", r2.Calls)
	}
}

func TestParseHostQuoteAndHostsFile(t *testing.T) {
	cases := map[string]target{
		"srv":           {dest: "srv"},
		"apps@srv:2222": {dest: "apps@srv", port: "2222"},
		"[::1]:2200":    {dest: "::1", port: "2200"},
		"fe80::1":       {dest: "fe80::1"},
		"user@10.0.0.1": {dest: "user@10.0.0.1"},
	}
	for in, want := range cases {
		if got, err := parseHost(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "srv:port"} {
		if _, err := parseHost(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if shellQuote("/a/b-c") != "/a/b-c" || shellQuote("a b") != "'a b'" || shellQuote("it's") != `'it'\''s'` || shellQuote("") != "''" || shellQuote("$(x)") != "'$(x)'" {
		t.Error("shellQuote")
	}
	p := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(p, []byte("# servers\nsrv-01\n\n  apps@web:2222  # web\n"), 0o644)
	if h, err := ReadHostsFile(p); err != nil || strings.Join(h, ",") != "srv-01,apps@web:2222" {
		t.Errorf("hosts file: %v %v", h, err)
	}
}
