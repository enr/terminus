package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/model"
)

// runCLI runs terminus isolated from the machine configuration: an empty configuration file and
// no external modules, unless the arguments set them.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	base := []string{"--config", writeConfig(t, ""), "--modules-dir", filepath.Join(t.TempDir(), "none")}
	var stdout, stderr bytes.Buffer
	code := run(append(base, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "terminus.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// externalDir creates an external facts directory with a static fact.
func externalDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker.json"), []byte(`{"ServerAPIVersion":"1.16"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNormalizeLegacyArgs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"System.Hostname"}, []string{"System.Hostname"}},
		{[]string{"-version"}, []string{"--version"}},
		{[]string{"-format", "{{.System.Hostname}}"}, []string{"--format", "{{.System.Hostname}}"}},
		{[]string{"-external-facts-dir=/tmp/x", "-http", ":6060"}, []string{"serve", "--external-facts-dir=/tmp/x", "--http", ":6060"}},
		{[]string{"serve", "--http=:6060"}, []string{"serve", "--http=:6060"}},
		{[]string{"check", "-v"}, []string{"check", "-v"}},
		{[]string{"probe", "--container", "web", "--http", "http://x/"}, []string{"probe", "--container", "web", "--http", "http://x/"}},
	}
	for _, c := range cases {
		if got := normalizeLegacyArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFactsPathQuery(t *testing.T) {
	dir := externalDir(t)
	code, out, errOut := runCLI(t, "--external-facts-dir", dir, "System.Kernel.Name")
	if code != 0 || strings.TrimSpace(out) != "Linux" {
		t.Fatalf("code %d, out %q, err %q", code, out, errOut)
	}
	code, out, _ = runCLI(t, "facts", "--external-facts-dir", dir, "docker.ServerAPIVersion")
	if code != 0 || strings.TrimSpace(out) != "1.16" {
		t.Fatalf("external fact: code %d, out %q", code, out)
	}
	code, _, errOut = runCLI(t, "facts", "--external-facts-dir", dir, "System.NoSuchFact")
	if code != model.ExitError || !strings.Contains(errOut, "not found") {
		t.Fatalf("missing fact: code %d, err %q", code, errOut)
	}
}

func TestFactsTemplate(t *testing.T) {
	dir := externalDir(t)
	code, out, errOut := runCLI(t, "-external-facts-dir", dir, "-format", "{{ .System.Kernel.Name }} {{ .system.Kernel.Name }} {{ .docker.ServerAPIVersion }}")
	if code != 0 || out != "Linux Linux 1.16" {
		t.Fatalf("code %d, out %q, err %q", code, out, errOut)
	}
}

func TestFactsJSON(t *testing.T) {
	code, out, errOut := runCLI(t, "facts", "-o", "json", "--external-facts-dir", "/nonexistent")
	if code != 0 {
		t.Fatalf("code %d, err %q", code, errOut)
	}
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.Modules["system"].Status != model.StatusOK {
		t.Errorf("system module: %+v", r.Modules["system"])
	}
	if r.Modules["external"].Status != model.StatusSkipped {
		t.Errorf("external module: %+v", r.Modules["external"])
	}
	if len(r.Findings) != 0 {
		t.Errorf("facts ran checks: %v", r.Findings)
	}
}

func TestCheck(t *testing.T) {
	code, out, errOut := runCLI(t, "check", "-o", "json", "--only", "memory,storage")
	var r model.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v (stderr %q)", err, errOut)
	}
	if len(r.Findings) == 0 {
		t.Fatal("no findings")
	}
	if code != r.ExitCode() {
		t.Errorf("exit code %d, report says %d", code, r.ExitCode())
	}
	if _, ok := r.Modules["external"]; ok {
		t.Error("--only ran the external module")
	}

	code, out, _ = runCLI(t, "check", "--only", "memory", "--color", "never")
	if !strings.Contains(out, "mem.available") || strings.Contains(out, "\x1b[") {
		t.Errorf("text output (code %d):\n%s", code, out)
	}
}

func TestErrors(t *testing.T) {
	for _, args := range [][]string{
		{"check", "--only", "nope"},
		{"check", "-o", "yaml"},
		{"--color", "sometimes"},
		{"facts", "--format", "{{ .Broken"},
	} {
		if code, _, errOut := runCLI(t, args...); code != model.ExitError || errOut == "" {
			t.Errorf("%v: code %d, stderr %q", args, code, errOut)
		}
	}
}

func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-version"}} {
		code, out, _ := runCLI(t, args...)
		if code != 0 || !strings.HasPrefix(out, "terminus ") {
			t.Errorf("%v: code %d, out %q", args, code, out)
		}
	}
}

func TestServeHandler(t *testing.T) {
	g := &globalOptions{externalFactsDir: externalDir(t), color: "never", configPath: writeConfig(t, ""),
		changed: func(n string) bool { return n == "config" || n == "external-facts-dir" }}
	var logs bytes.Buffer
	a, err := newApp(g, &logs)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newHandler(a, slog.New(slog.NewTextHandler(&logs, nil)), 0))
	defer srv.Close()

	res, err := http.Post(srv.URL+"/facts", "text/plain", strings.NewReader("System.Kernel.Name"))
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, res)
	if res.StatusCode != http.StatusOK || body != "Linux" {
		t.Fatalf("/facts: %d %q", res.StatusCode, body)
	}

	res, err = http.Post(srv.URL+"/facts", "text/plain", strings.NewReader("Nope.Nope"))
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("/facts missing: %d", res.StatusCode)
	}

	res, err = http.Get(srv.URL + "/report")
	if err != nil {
		t.Fatal(err)
	}
	var r model.Report
	if err := json.Unmarshal([]byte(readBody(t, res)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Modules["system"].Status != model.StatusOK || len(r.Findings) == 0 {
		t.Fatalf("/report: %+v", r.Summary)
	}

	res, err = http.Get(srv.URL + "/facts")
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /facts: %d, want 405", res.StatusCode)
	}

	res, err = http.Post(srv.URL+"/report", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, res)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /report: %d, want 405", res.StatusCode)
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	var b bytes.Buffer
	if _, err := b.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
