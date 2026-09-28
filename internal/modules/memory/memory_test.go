package memory

import (
	"context"
	"testing"

	"github.com/enr/terminus/internal/hostfs/hostfstest"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

const meminfo = `MemTotal:        4000000 kB
MemFree:          100000 kB
MemAvailable:     300000 kB
Buffers:           10000 kB
Cached:           150000 kB
SwapTotal:       1000000 kB
SwapFree:         100000 kB
Dirty:               100 kB
Shmem:              5000 kB
HugePages_Total:       0
Hugepagesize:       2048 kB
`

func collect(t *testing.T, files map[string]string) (*Facts, error) {
	t.Helper()
	got, err := (&Module{fs: hostfstest.New(t, files)}).Collect(context.Background(), &module.Env{})
	if got == nil {
		return nil, err
	}
	return got.(*Facts), err
}

func TestCollectAndCheck(t *testing.T) {
	f, err := collect(t, map[string]string{
		"/proc/meminfo":         meminfo,
		"/proc/vmstat":          "nr_free_pages 1\noom_kill 2\n",
		"/proc/pressure/memory": "some avg10=0 avg60=0 avg300=8.00 total=1\nfull avg10=0 avg60=1 avg300=6.00 total=1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.TotalBytes != 4000000*1024 || f.AvailableBytes != 300000*1024 || f.Meminfo["HugePages_Total"] != 0 || f.Meminfo["Hugepagesize"] != 2048*1024 {
		t.Errorf("facts: %+v", f)
	}
	if f.AvailableRatio != 0.075 || f.SwapUsedRatio != 0.9 || f.OOMKills == nil || *f.OOMKills != 2 {
		t.Errorf("derived: %v %v %v", f.AvailableRatio, f.SwapUsedRatio, f.OOMKills)
	}

	want := map[string]model.Severity{
		"mem.available": model.SeverityWarn,
		"mem.swap-used": model.SeverityFail,
		"mem.oom-kills": model.SeverityWarn,
		"mem.pressure":  model.SeverityWarn,
	}
	got := (&Module{}).Check(nil, f)
	if len(got) != len(want) {
		t.Fatalf("findings: %+v", got)
	}
	for _, x := range got {
		if want[x.ID] != x.Severity || x.Hint == "" {
			t.Errorf("%s: %s hint %q", x.ID, x.Severity, x.Hint)
		}
	}
}

func TestHealthyAndOldKernel(t *testing.T) {
	f, err := collect(t, map[string]string{
		"/proc/meminfo": "MemTotal: 1000 kB\nMemAvailable: 900 kB\nSwapTotal: 0 kB\n",
		"/proc/vmstat":  "nr_free_pages 1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := (&Module{}).Check(nil, f)
	if len(got) != 1 || got[0].ID != "mem.available" || got[0].Severity != model.SeverityOK {
		t.Errorf("findings: %+v", got)
	}
}

func TestErrors(t *testing.T) {
	if f, err := collect(t, nil); err == nil || f != nil {
		t.Errorf("missing meminfo: %v %v", f, err)
	}
	if _, err := collect(t, map[string]string{"/proc/meminfo": "MemFree: 1 kB\n"}); err == nil {
		t.Error("meminfo without MemTotal accepted")
	}
	if _, err := collect(t, map[string]string{"/proc/meminfo": "MemTotal: x kB\n"}); err == nil {
		t.Error("invalid meminfo accepted")
	}
	f, err := collect(t, map[string]string{"/proc/meminfo": "MemTotal: 1000 kB\n"})
	if err == nil || f == nil {
		t.Errorf("missing vmstat must give a partial result: %v %v", f, err)
	}
}
