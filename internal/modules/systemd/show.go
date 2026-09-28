package systemd

import (
	"strconv"
	"strings"
)

// showProperties are the properties read with systemctl show.
var showProperties = []string{
	"Id", "LoadState", "ActiveState", "SubState", "Result", "NRestarts",
	"ExecMainCode", "ExecMainStatus",
	"MemoryCurrent", "MemoryPeak", "MemoryMax", "MemoryHigh",
	"TasksCurrent", "TasksMax", "CPUUsageNSec",
	"ControlGroup", "FragmentPath", "SourcePath", "ActiveEnterTimestamp",
}

// notSet is how systemd prints an unset uint64 (UINT64_MAX).
const notSet = "18446744073709551615"

// listedUnit is a line of systemctl list-units.
type listedUnit struct {
	Name, Load, Active, Sub string
}

// parseListUnits parses `systemctl list-units --all --plain --no-legend --full`:
// UNIT LOAD ACTIVE SUB DESCRIPTION, with a "●" marker before failed units on some versions.
func parseListUnits(lines []string) []listedUnit {
	var out []listedUnit
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) > 0 && (f[0] == "●" || f[0] == "*") {
			f = f[1:]
		}
		if len(f) < 4 {
			continue
		}
		out = append(out, listedUnit{Name: f[0], Load: f[1], Active: f[2], Sub: f[3]})
	}
	return out
}

// parseShow parses `systemctl show` output for several units: blocks of Key=Value separated by
// blank lines.
func parseShow(lines []string) []map[string]string {
	var out []map[string]string
	cur := map[string]string{}
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur = map[string]string{}
		}
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if ok {
			cur[k] = v
		}
	}
	flush()
	return out
}

// bytesValue parses a memory property: nil when not set, unlimited ("infinity") or unknown.
func bytesValue(v string) *uint64 {
	if v == "" || v == "[not set]" || v == "infinity" || v == notSet {
		return nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// unitFromShow builds a unit from its show properties.
func unitFromShow(p map[string]string) Unit {
	u := Unit{
		Name:         p["Id"],
		Load:         p["LoadState"],
		Active:       p["ActiveState"],
		Sub:          p["SubState"],
		Result:       p["Result"],
		MainExitCode: exitCodeName(p["ExecMainCode"]),
		ControlGroup: p["ControlGroup"],
		FragmentPath: p["FragmentPath"],
		SourcePath:   p["SourcePath"],
		ActiveSince:  p["ActiveEnterTimestamp"],

		MemoryCurrentBytes: bytesValue(p["MemoryCurrent"]),
		MemoryPeakBytes:    bytesValue(p["MemoryPeak"]),
		MemoryMaxBytes:     bytesValue(p["MemoryMax"]),
		MemoryHighBytes:    bytesValue(p["MemoryHigh"]),
		TasksCurrent:       bytesValue(p["TasksCurrent"]),
		TasksMax:           bytesValue(p["TasksMax"]),
	}
	if n, err := strconv.Atoi(p["NRestarts"]); err == nil {
		u.Restarts = &n
	}
	if n, err := strconv.Atoi(p["ExecMainStatus"]); err == nil {
		u.MainExitStatus = n
	}
	if ns := bytesValue(p["CPUUsageNSec"]); ns != nil {
		s := float64(*ns) / 1e9
		u.CPUUsageSeconds = &s
	}
	return u
}

// exitCodeName maps ExecMainCode (a CLD_* number) to the names used by the journal.
func exitCodeName(v string) string {
	switch v {
	case "1":
		return "exited"
	case "2":
		return "killed"
	case "3":
		return "dumped"
	default:
		return ""
	}
}

// parseVersion reads the first line of systemctl --version: "systemd 255 (255.4-1ubuntu8)".
func parseVersion(out string) int {
	f := strings.Fields(out)
	if len(f) < 2 || f[0] != "systemd" {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}
