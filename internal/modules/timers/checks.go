package timers

import (
	"fmt"
	"strings"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "timers.failed", Description: "fail: the last run of the unit a timer starts failed, or the unit does not exist"},
		{ID: "timers.not-active", Description: "warn: a timer enabled but not started: it does not fire until the next boot"},
		{ID: "timers.never-run", Description: "info: a timer that never fired although active for longer than the wait to its next run"},
		{ID: "timers.no-next", Description: "info: an active timer that will not fire again"},
		{ID: "timers.scope", Description: "a user manager not inspected, or not readable"},
	}
}

// Check implements module.Checker.
func (m *Module) Check(_ *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	var out []model.Finding
	for _, mg := range f.Managers {
		switch {
		case mg.Skipped != "":
			out = append(out, model.Finding{ID: "timers.scope", Subject: mg.Name, Severity: model.SeverityInfo, Message: "not inspected: " + mg.Skipped})
			continue
		case mg.Error != "":
			out = append(out, model.Finding{ID: "timers.scope", Subject: mg.Name, Severity: model.SeverityWarn, Message: mg.Error})
			continue
		}
		failed := 0
		for _, t := range mg.Timers {
			subject := mg.Name + ":" + t.Name
			u := t.Unit
			switch {
			case u.Load == "not-found":
				failed++
				out = append(out, model.Finding{
					ID: "timers.failed", Subject: subject, Severity: model.SeverityFail,
					Message: "starts " + u.Name + ", which does not exist",
					Hint:    "Unit= in the timer, or the service file is missing (systemctl daemon-reload?)",
				})
			case u.Active == "failed" || (u.Result != "" && u.Result != "success"):
				failed++
				fnd := model.Finding{
					ID: "timers.failed", Subject: subject, Severity: model.SeverityFail,
					Message:  fmt.Sprintf("the last run of %s failed (result %s, exit status %d)", u.Name, u.Result, u.ExitStatus),
					Evidence: map[string]any{"last_run": t.LastRun},
					Hint:     journalHint(mg, u.Name),
				}
				out = append(out, fnd)
			}

			switch {
			case t.Active != "active" && t.UnitFileState == "enabled":
				out = append(out, model.Finding{
					ID: "timers.not-active", Subject: subject, Severity: model.SeverityWarn,
					Message: fmt.Sprintf("enabled but %s: it does not fire until the next boot", t.Active),
					Hint:    "systemctl " + userFlag(mg) + "start " + t.Name,
				})
			case t.Active != "active":
			case t.NextRun.IsZero() && !hasMonotonic(t.Schedule):
				out = append(out, model.Finding{
					ID: "timers.no-next", Subject: subject, Severity: model.SeverityInfo,
					Message:  "active but it will not fire again (" + strings.Join(t.Schedule, ", ") + ")",
					Evidence: map[string]any{"last_run": t.LastRun},
				})
			case t.LastRun.IsZero() && !t.ActiveSince.IsZero() && !t.NextRun.IsZero() &&
				now.Sub(t.ActiveSince) > t.NextRun.Sub(now) && now.Sub(t.ActiveSince) > 25*time.Hour:
				out = append(out, model.Finding{
					ID: "timers.never-run", Subject: subject, Severity: model.SeverityInfo,
					Message: fmt.Sprintf("never fired in %.0f days, next run %s", now.Sub(t.ActiveSince).Hours()/24, t.NextRun.Format("2006-01-02 15:04")),
				})
			}
		}
		if failed == 0 && len(mg.Timers) > 0 {
			out = append(out, model.Finding{
				ID: "timers.failed", Subject: mg.Name, Severity: model.SeverityOK,
				Message: fmt.Sprintf("the last runs of %d timers succeeded", len(mg.Timers)),
			})
		}
	}
	return out
}

// hasMonotonic tells whether a timer has relative triggers: after they elapse, systemd shows no
// next run until the reference event (boot, activation) happens again.
func hasMonotonic(schedule []string) bool {
	for _, s := range schedule {
		if !strings.HasPrefix(s, "OnCalendar=") {
			return true
		}
	}
	return false
}

func userFlag(mg Manager) string {
	if mg.User != "" {
		return "--user "
	}
	return ""
}

func journalHint(mg Manager, unit string) string {
	if mg.User != "" {
		return "journalctl --user -u " + unit + " -n 50 (as " + mg.User + ")"
	}
	return "journalctl -u " + unit + " -n 50"
}
