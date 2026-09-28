package updates

import (
	"fmt"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Default thresholds.
var (
	securityThreshold = module.Threshold{Warn: 1, Fail: 20, Unit: "packages"}
	indexThreshold    = module.Threshold{Warn: 7, Fail: 30, Unit: "days"}
)

// maxListed bounds the package names in a message.
const maxListed = 8

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "updates.security", Description: "pending security updates", Threshold: &securityThreshold},
		{ID: "updates.pending", Description: "info: pending updates"},
		{ID: "updates.reboot", Description: "warn: a reboot is pending (marker file, needs-restarting, newer kernel installed)"},
		{ID: "updates.index-age", Description: "days since the package index was refreshed: the counts are only as recent as the index", Threshold: &indexThreshold},
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	if f.SecurityKnown {
		var names []string
		for _, p := range f.Pending {
			if p.Security {
				names = append(names, p.Name)
			}
		}
		fnd := model.Finding{
			ID:       "updates.security",
			Subject:  f.Manager,
			Severity: env.Threshold("updates.security", securityThreshold).Grade(float64(f.SecurityCount)),
			Message:  "no pending security updates",
		}
		if f.SecurityCount > 0 {
			fnd.Message = fmt.Sprintf("%d pending security updates: %s", f.SecurityCount, list(names))
			fnd.Hint = "install them (apt upgrade, dnf upgrade --security), or enable unattended-upgrades / dnf-automatic"
		}
		out = append(out, fnd)
	}

	pending := model.Finding{ID: "updates.pending", Subject: f.Manager, Severity: model.SeverityOK, Message: "the system is up to date"}
	if f.PendingCount > 0 {
		var names []string
		for _, p := range f.Pending {
			names = append(names, p.Name)
		}
		pending.Severity = model.SeverityInfo
		pending.Message = fmt.Sprintf("%d pending updates: %s", f.PendingCount, list(names))
	}
	out = append(out, pending)

	reboot := model.Finding{ID: "updates.reboot", Subject: "kernel " + f.RunningKernel, Severity: model.SeverityOK, Message: "no reboot pending"}
	if f.RunningKernel == "" {
		reboot.Subject = "system"
	}
	if f.RebootRequired {
		reboot.Severity = model.SeverityWarn
		reboot.Message = "reboot pending: " + strings.Join(f.RebootReasons, "; ")
		reboot.Hint = "the updates are installed but not in effect until the reboot"
	}
	out = append(out, reboot)

	if !f.IndexUpdated.IsZero() {
		days := f.IndexAgeSeconds / 86400
		fnd := model.Finding{
			ID:       "updates.index-age",
			Subject:  f.Manager,
			Severity: env.Threshold("updates.index-age", indexThreshold).Grade(days),
			Message:  fmt.Sprintf("package index refreshed %.1f days ago (%s)", days, f.IndexUpdated.Local().Format("2006-01-02 15:04")),
		}
		if fnd.Severity >= model.SeverityWarn {
			fnd.Hint = "the pending updates are unknown past this date: is apt-daily.timer / dnf-makecache.timer running?"
		}
		out = append(out, fnd)
	}
	return out
}

func list(names []string) string {
	if len(names) <= maxListed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxListed], ", "), len(names)-maxListed)
}
