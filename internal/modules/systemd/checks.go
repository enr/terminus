package systemd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Default thresholds.
var (
	memoryPeakThreshold = module.Threshold{Warn: 0.9, Fail: 1.0, Unit: "ratio"}
	restartsThreshold   = module.Threshold{Warn: 3, Fail: 10, Unit: "restarts"}
	overcommitThreshold = module.Threshold{Warn: 1.0, Fail: 1.5, Unit: "ratio"}
)

// minPeakVersion is the first systemd reporting MemoryPeak.
const minPeakVersion = 253

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "systemd.failed", Description: "units in the failed state, in the system and user managers"},
		{ID: "unit.memory-peak", Description: "memory peak of a unit over its MemoryMax", Threshold: &memoryPeakThreshold},
		{ID: "unit.memory-limit", Description: "services of user managers without MemoryMax (info)"},
		{ID: "unit.oom-kills", Description: "processes of a unit killed by the OOM killer in the history window"},
		{ID: "unit.restarts", Description: "automatic restarts of a unit (NRestarts), with the exit reasons", Threshold: &restartsThreshold},
		{ID: "user.linger", Description: "users with enabled services but without linger: the services stop at logout"},
		{ID: "resources.overcommit", Description: "sum of the MemoryMax of the running services over the RAM", Threshold: &overcommitThreshold},
		{ID: "systemd.version", Description: "systemd or kernel too old for some facts (info)"},
	}
}

func subject(mg Manager, unit string) string { return mg.Name + ":" + unit }

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	out = append(out, failedFindings(f)...)
	out = append(out, lingerFindings(f)...)

	peakT := env.Threshold("unit.memory-peak", memoryPeakThreshold)
	restartT := env.Threshold("unit.restarts", restartsThreshold)
	var limitsSum uint64
	var limited []string
	for _, mg := range f.Managers {
		for _, u := range mg.Units {
			out = append(out, unitFindings(mg, u, peakT, restartT)...)
			if u.Active == "active" && u.MemoryMaxBytes != nil && strings.HasSuffix(u.Name, ".service") {
				limitsSum += *u.MemoryMaxBytes
				limited = append(limited, subject(mg, u.Name))
			}
		}
	}

	if f.MemTotalBytes > 0 && limitsSum > 0 {
		ratio := float64(limitsSum) / float64(f.MemTotalBytes)
		o := model.Finding{
			ID:       "resources.overcommit",
			Subject:  "MemoryMax",
			Severity: env.Threshold("resources.overcommit", overcommitThreshold).Grade(ratio),
			Message:  fmt.Sprintf("the memory limits of %d services add up to %.0f%% of the RAM", len(limited), ratio*100),
			Evidence: map[string]any{"limits_bytes": limitsSum, "mem_total_bytes": f.MemTotalBytes, "limit_ratio": ratio},
		}
		if o.Severity >= model.SeverityWarn {
			o.Hint = "the limits cannot all be reached at once: the OOM killer, not the limits, decides who dies"
		}
		out = append(out, o)
	}

	var missing []string
	if f.Version > 0 && f.Version < minPeakVersion {
		missing = append(missing, fmt.Sprintf("systemd %d does not report MemoryPeak (253+)", f.Version))
	}
	if !f.CgroupV2 {
		missing = append(missing, "cgroup v2 is not mounted: per-unit memory peaks and OOM counters are not available")
	}
	if f.HistoryError != "" {
		missing = append(missing, "journal history not available: "+f.HistoryError)
	}
	for _, mg := range f.Managers {
		if mg.Skipped == "requires root" {
			missing = append(missing, mg.Name+" not inspected: requires root")
		}
	}
	if len(missing) > 0 {
		out = append(out, model.Finding{
			ID:       "systemd.version",
			Subject:  "systemd",
			Severity: model.SeverityInfo,
			Message:  "some facts are not available: " + strings.Join(missing, "; "),
		})
	}
	return out
}

func failedFindings(f *Facts) []model.Finding {
	var out []model.Finding
	inspected := 0
	for _, mg := range f.Managers {
		if mg.Skipped != "" {
			continue
		}
		inspected++
		if mg.Error != "" {
			out = append(out, model.Finding{
				ID:       "systemd.failed",
				Subject:  mg.Name,
				Severity: model.SeverityWarn,
				Message:  "cannot list the units: " + mg.Error,
			})
			continue
		}
		for _, u := range mg.Failed {
			out = append(out, model.Finding{
				ID:       "systemd.failed",
				Subject:  subject(mg, u),
				Severity: model.SeverityFail,
				Message:  "unit is failed",
				Hint:     statusHint(mg, u),
			})
		}
	}
	if len(out) == 0 {
		out = append(out, model.Finding{
			ID:       "systemd.failed",
			Subject:  "units",
			Severity: model.SeverityOK,
			Message:  fmt.Sprintf("no failed units in %d managers", inspected),
		})
	}
	return out
}

func statusHint(mg Manager, unit string) string {
	if mg.User != "" {
		return fmt.Sprintf("systemctl --user -M %s@ status %s; journalctl --user-unit %s", mg.User, unit, unit)
	}
	return fmt.Sprintf("systemctl status %s; journalctl -u %s", unit, unit)
}

func lingerFindings(f *Facts) []model.Finding {
	var out []model.Finding
	for _, u := range f.Users {
		fnd := model.Finding{ID: "user.linger", Subject: u.Name, Evidence: map[string]any{"enabled_units": u.EnabledUnits, "manager_running": u.ManagerRunning}}
		switch {
		case u.Linger:
			fnd.Severity = model.SeverityOK
			fnd.Message = "linger enabled: the user services run without a session"
		case u.EnabledUnits > 0 || u.ManagerRunning:
			fnd.Severity = model.SeverityFail
			fnd.Message = "linger disabled: the user services stop when the last session ends and do not start at boot"
			fnd.Hint = "loginctl enable-linger " + u.Name
		default:
			continue
		}
		out = append(out, fnd)
	}
	return out
}

func unitFindings(mg Manager, u Unit, peakT, restartT module.Threshold) []model.Finding {
	var out []model.Finding
	subj := subject(mg, u.Name)

	if u.MemoryMaxBytes != nil && u.MemoryPeakBytes != nil && *u.MemoryMaxBytes > 0 {
		ratio := float64(*u.MemoryPeakBytes) / float64(*u.MemoryMaxBytes)
		f := model.Finding{
			ID:       "unit.memory-peak",
			Subject:  subj,
			Severity: peakT.Grade(ratio),
			Message:  fmt.Sprintf("memory peak at %.0f%% of MemoryMax", ratio*100),
			Evidence: map[string]any{"memory_peak_bytes": *u.MemoryPeakBytes, "memory_max_bytes": *u.MemoryMaxBytes},
		}
		if u.MemoryCurrentBytes != nil {
			f.Evidence["memory_current_bytes"] = *u.MemoryCurrentBytes
		}
		if f.Severity >= model.SeverityWarn {
			f.Hint = "the peak is since the unit started: the cgroup reclaims cache before killing, check unit.oom-kills before raising the limit"
		}
		out = append(out, f)
	} else if mg.User != "" && u.Active == "active" && u.MemoryMaxBytes == nil && strings.HasSuffix(u.Name, ".service") && u.Load != "" {
		out = append(out, model.Finding{
			ID:       "unit.memory-limit",
			Subject:  subj,
			Severity: model.SeverityInfo,
			Message:  "no MemoryMax: the service can use all the memory of the machine",
			Hint:     "set MemoryMax= in the unit (or in [Service] of the quadlet file)",
		})
	}

	oom := 0
	if u.History != nil {
		oom = u.History.OOMKills
	}
	if u.OOMKills != nil && int(*u.OOMKills) > oom {
		oom = int(*u.OOMKills)
	}
	if oom > 0 {
		f := model.Finding{
			ID:       "unit.oom-kills",
			Subject:  subj,
			Severity: model.SeverityFail,
			Message:  fmt.Sprintf("%d OOM kills", oom),
			Evidence: map[string]any{},
			Hint:     "raise MemoryMax or reduce the memory use; the memory peak shows how close it runs to the limit",
		}
		if u.History != nil && u.History.LastOOMKill != "" {
			f.Message += ", last at " + u.History.LastOOMKill
			f.Evidence["last_oom_kill"] = u.History.LastOOMKill
		}
		if u.OOMKills != nil {
			f.Evidence["oom_kills_current_cgroup"] = *u.OOMKills
		}
		out = append(out, f)
	}

	if u.Restarts != nil && *u.Restarts > 0 {
		f := model.Finding{
			ID:       "unit.restarts",
			Subject:  subj,
			Severity: restartT.Grade(float64(*u.Restarts)),
			Message:  fmt.Sprintf("restarted automatically %d times since it was started", *u.Restarts),
			Evidence: map[string]any{"restarts": *u.Restarts},
		}
		if u.History != nil && len(u.History.Exits) > 0 {
			f.Message += "; exits: " + describeExits(u.History.Exits)
			f.Evidence["exits"] = u.History.Exits
		}
		if f.Severity >= model.SeverityWarn {
			f.Hint = statusHint(mg, u.Name)
		}
		out = append(out, f)
	}
	return out
}

// describeExits lists the exits, the most frequent first: "143 (SIGTERM: stop or deploy) ×4, exit 1 ×1".
func describeExits(exits map[string]int) string {
	keys := make([]string, 0, len(exits))
	for k := range exits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if exits[keys[i]] != exits[keys[j]] {
			return exits[keys[i]] > exits[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s ×%d", describeExit(k), exits[k])
	}
	return strings.Join(parts, ", ")
}
