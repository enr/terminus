package journal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/enr/terminus/internal/config"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/runner"
)

const args = "journalctl --no-pager -o json --since=-43200s --output-fields=_SYSTEMD_UNIT,_SYSTEMD_USER_UNIT,_UID"

func lines(unit string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = unit
	}
	return out
}

func TestCollectAndCheck(t *testing.T) {
	fs := hostfstest.New(t, map[string]string{
		"/var/log/journal/abc/system.journal":    strings.Repeat("x", 3000),
		"/var/log/journal/abc/user-1001.journal": strings.Repeat("x", 1000),
		"/var/log/journal/abc/system@1.journal~": strings.Repeat("x", 1000),
		"/var/log/journal/abc/notes.txt":         strings.Repeat("x", 9999),
		"/run/log/journal/def/system.journal":    strings.Repeat("x", 500),
	})
	var out []string
	out = append(out, lines(`{"_SYSTEMD_USER_UNIT":"myapp.service","_UID":"1001"}`, 40)...)
	out = append(out, lines(`{"_SYSTEMD_UNIT":"cron.service","_UID":"0"}`, 3)...)
	out = append(out, lines(`{"_UID":"0"}`, 2)...)
	out = append(out, "garbage")
	r := &runner.Fake{
		Paths:   map[string]string{"journalctl": "/usr/bin/journalctl"},
		Results: map[string]runner.Result{args: {Stdout: []byte(strings.Join(out, "\n") + "\n")}},
	}
	m := New()
	m.fs = fs
	m.statfs = func(string) (unix.Statfs_t, error) { return unix.Statfs_t{Bsize: 1000, Blocks: 40}, nil }
	m.lookup = func(uid string) (runner.Account, error) {
		if uid == "1001" {
			return runner.Account{Name: "apps", UID: 1001}, nil
		}
		return runner.Account{}, errors.New("unknown")
	}
	c, _ := config.Parse("[modules.journal]\nwindow = \"12h\"\n")
	if err := m.Configure(c.Decoder(Name)); err != nil {
		t.Fatal(err)
	}

	got, err := m.Collect(context.Background(), &module.Env{Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.DiskUsageBytes != 5500 || f.Directory != "/var/log/journal" || f.FilesystemSizeBytes != 40000 || f.TotalLines != 45 {
		t.Fatalf("facts: %+v", f)
	}
	if f.Units[0] != (UnitLines{Name: "user:apps:myapp.service", Lines: 40, LinesPerDay: 80}) || f.Units[1].Name != "system:cron.service" || f.Units[2].Name != "other" {
		t.Errorf("units: %+v", f.Units)
	}

	env := &module.Env{Checks: &module.CheckSettings{Thresholds: map[string]module.Threshold{"journal.noisy-unit": {Warn: 50, Fail: 100}}}}
	sev := map[string]model.Severity{}
	for _, x := range m.Check(env, f) {
		sev[x.ID+" "+x.Subject] = x.Severity
	}
	want := map[string]model.Severity{
		"journal.disk-usage /var/log/journal":        model.SeverityWarn, // 13.75%
		"journal.noisy-unit user:apps:myapp.service": model.SeverityWarn,
	}
	if len(sev) != len(want) {
		t.Errorf("findings: %v", sev)
	}
	for k, v := range want {
		if sev[k] != v {
			t.Errorf("%s: %s, want %s", k, sev[k], v)
		}
	}
	quiet := m.Check(nil, f)
	if quiet[len(quiet)-1].Severity != model.SeverityOK || !strings.Contains(quiet[len(quiet)-1].Message, "myapp") {
		t.Errorf("quiet summary: %+v", quiet)
	}
}

func TestSkipDetectAndConfig(t *testing.T) {
	m := New()
	m.fs = hostfstest.New(t, map[string]string{"/run/log/journal/x": ""})
	none := &runner.Fake{}
	if _, err := m.Collect(context.Background(), &module.Env{Runner: none}); err == nil || !strings.Contains(err.Error(), "journalctl not found") {
		t.Errorf("skip: %v", err)
	}
	if d := m.Detect(context.Background(), &module.Env{Runner: none}); d.Found {
		t.Errorf("detect without journalctl: %+v", d)
	}
	withJ := &runner.Fake{Paths: map[string]string{"journalctl": "/bin/journalctl"}}
	if d := m.Detect(context.Background(), &module.Env{Runner: withJ}); !d.Found || !strings.Contains(d.Config, "enabled = true") {
		t.Errorf("detect: %+v", d)
	}
	c, _ := config.Parse("[modules.journal]\nwindow = \"-1h\"\n")
	if err := m.Configure(c.Decoder(Name)); err == nil {
		t.Error("negative window accepted")
	}
}
