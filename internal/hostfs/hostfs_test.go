package hostfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseKeyValue(t *testing.T) {
	m := ParseKeyValue([]string{
		`# comment`,
		``,
		`NAME="Fedora Linux"`,
		`ID=fedora`,
		`VERSION_CODENAME=''`,
		`PRETTY_NAME="A \"quoted\" \$name"`,
		`SINGLE='a "b"'`,
		`broken line`,
	})
	want := map[string]string{
		"NAME":             "Fedora Linux",
		"ID":               "fedora",
		"VERSION_CODENAME": "",
		"PRETTY_NAME":      `A "quoted" $name`,
		"SINGLE":           `a "b"`,
	}
	if len(m) != len(want) {
		t.Fatalf("got %v", m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
}

func TestParsePSI(t *testing.T) {
	p, err := ParsePSI([]string{
		"some avg10=1.50 avg60=0.25 avg300=0.00 total=12345",
		"full avg10=0.00 avg60=0.10 avg300=0.00 total=99",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Some == nil || p.Some.Avg10 != 1.5 || p.Some.TotalMicros != 12345 || p.Full == nil || p.Full.Avg60 != 0.1 {
		t.Fatalf("got %+v %+v", p.Some, p.Full)
	}
	if _, err := ParsePSI([]string{"some avg10=x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestFS(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "proc/pressure"), 0o755))
	must(os.WriteFile(filepath.Join(root, "proc/pressure/cpu"), []byte("some avg10=0.00 avg60=2.00 avg300=0.00 total=1\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "proc/n"), []byte(" 42\n"), 0o644))
	f := FS{Root: root}

	if n, err := f.ReadUint("/proc/n"); err != nil || n != 42 {
		t.Fatalf("ReadUint = %d %v", n, err)
	}
	p, err := f.ReadPSI("cpu")
	if err != nil || p == nil || p.Some.Avg60 != 2 {
		t.Fatalf("ReadPSI = %+v %v", p, err)
	}
	if p, err := f.ReadPSI("io"); p != nil || err != nil {
		t.Fatalf("missing PSI = %+v %v", p, err)
	}
	if !f.Exists("/proc/n") || f.Exists("/proc/none") {
		t.Fatal("Exists")
	}
	if Host.Path("/etc/os-release") != "/etc/os-release" {
		t.Fatal("Host.Path")
	}
}
