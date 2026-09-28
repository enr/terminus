package system

import (
	"testing"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/lib/facts"
)

func TestCheckMemory(t *testing.T) {
	cases := []struct {
		available uint64
		want      model.Severity
	}{
		{500, model.SeverityOK},
		{90, model.SeverityWarn},
		{40, model.SeverityFail},
	}
	for _, c := range cases {
		fs := checkMemory(facts.Memory{Total: 1000, Available: c.available})
		if len(fs) != 1 {
			t.Fatalf("available %d: %d findings", c.available, len(fs))
		}
		if fs[0].Severity != c.want {
			t.Errorf("available %d: severity %s, want %s", c.available, fs[0].Severity, c.want)
		}
	}
	if fs := checkMemory(facts.Memory{Total: 1000}); len(fs) != 0 {
		t.Errorf("unknown available memory produced findings: %v", fs)
	}
}
