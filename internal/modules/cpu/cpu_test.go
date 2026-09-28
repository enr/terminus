package cpu

import (
	"context"
	"testing"

	"github.com/enr/terminus/internal/hostfs"
	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
)

const x86CPUInfo = `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
cpu MHz		: 2400.000
physical id	: 0
core id		: 0
flags		: fpu vme hypervisor

processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
cpu MHz		: 2400.000
physical id	: 0
core id		: 1
flags		: fpu vme hypervisor

processor	: 2
physical id	: 0
core id		: 0

processor	: 3
physical id	: 0
core id		: 1
`

const armCPUInfo = `processor	: 0
BogoMIPS	: 48.00
Features	: fp asimd evtstrm
CPU implementer	: 0x41

processor	: 1
BogoMIPS	: 48.00
Features	: fp asimd evtstrm
CPU implementer	: 0x41
`

func TestParseCPUInfo(t *testing.T) {
	var f Facts
	parseCPUInfo(hostfs.SplitLines([]byte(x86CPUInfo)), &f)
	if f.Logical != 4 || f.Cores != 2 || f.Sockets != 1 || f.MHz != 2400 || f.Vendor != "GenuineIntel" || len(f.Flags) != 3 {
		t.Errorf("x86: %+v", f)
	}
	// The last processor has no trailing blank line: v1 lost it.
	var a Facts
	parseCPUInfo(hostfs.SplitLines([]byte(armCPUInfo)), &a)
	if a.Logical != 2 || a.Vendor != "0x41" || a.Flags[0] != "fp" || a.Sockets != 0 {
		t.Errorf("arm: %+v", a)
	}
}

func TestCollectAndCheck(t *testing.T) {
	fs := hostfstest.New(t, map[string]string{
		"/proc/cpuinfo":                  x86CPUInfo,
		"/sys/devices/system/cpu/online": "0-3\n",
		"/proc/loadavg":                  "3.00 5.00 6.00 3/412 9999\n",
		"/proc/pressure/cpu":             "some avg10=10.00 avg60=25.00 avg300=30.00 total=1\n",
	})
	got, err := (&Module{fs: fs}).Collect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := got.(*Facts)
	if f.Online != "0-3" || f.Load.Avg15 != 6 || f.Load.RunnableTasks != 3 || f.Load.TotalTasks != 412 || f.Load.Avg15PerCPU != 1.5 {
		t.Errorf("facts: %+v", f)
	}
	fs2 := (&Module{}).Check(nil, f)
	want := map[string]model.Severity{"cpu.load": model.SeverityWarn, "cpu.pressure": model.SeverityWarn}
	if len(fs2) != len(want) {
		t.Fatalf("findings: %+v", fs2)
	}
	for _, x := range fs2 {
		if want[x.ID] != x.Severity || x.Hint == "" {
			t.Errorf("%s: %s (hint %q)", x.ID, x.Severity, x.Hint)
		}
	}
}

func TestCollectMissing(t *testing.T) {
	got, err := (&Module{fs: hostfstest.New(t, nil)}).Collect(context.Background(), nil)
	if err == nil || got == nil {
		t.Fatalf("expected partial result, got %v %v", got, err)
	}
	if fs := (&Module{}).Check(nil, got); len(fs) != 0 {
		t.Errorf("findings without data: %v", fs)
	}
}

func TestParseLoadAvgErrors(t *testing.T) {
	for _, s := range []string{"", "1 2", "x 1 2 1/2 3"} {
		if _, err := parseLoadAvg(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}
