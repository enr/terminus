package backup

import (
	"fmt"
	"time"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

var ageThreshold = module.Threshold{Warn: 26, Fail: 50, Unit: "hours"}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "backup.age", Description: "hours since the latest backup; max_age of a repository sets warn at max_age and fail at twice", Threshold: &ageThreshold},
		{ID: "backup.repository", Description: "the repository cannot be read, or holds no backup"},
		{ID: "backup.job", Description: "the systemd unit that makes the backups failed its last run"},
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	def := env.Threshold("backup.age", ageThreshold)
	var out []model.Finding
	for _, r := range f.Repositories {
		subject := r.Type + ":" + r.Name
		if j := r.Job; j != nil {
			out = append(out, jobFinding(subject, j))
		}
		switch {
		case r.Error != "":
			out = append(out, model.Finding{
				ID:       "backup.repository",
				Subject:  subject,
				Severity: model.SeverityFail,
				Message:  "cannot read the repository: " + r.Error,
				Evidence: map[string]any{"repository": r.Repository},
				Hint:     "run the same command by hand as the same user: password, credentials (env_file), network, lock?",
			})
			continue
		case r.Latest == nil:
			out = append(out, model.Finding{
				ID:       "backup.repository",
				Subject:  subject,
				Severity: model.SeverityFail,
				Message:  "no backups in the repository",
				Evidence: map[string]any{"repository": r.Repository},
			})
			continue
		}
		t := def
		if r.MaxAgeSeconds > 0 {
			h := r.MaxAgeSeconds / 3600
			t.Warn, t.Fail = h, 2*h
		}
		b := r.Latest
		hours := r.AgeSeconds / 3600
		fnd := model.Finding{
			ID:       "backup.age",
			Subject:  subject,
			Severity: t.Grade(hours),
			Message:  fmt.Sprintf("latest backup %s ago (%s, %s)", age(r.AgeSeconds), b.ID, b.Time.Local().Format("2006-01-02 15:04")),
			Evidence: map[string]any{"repository": r.Repository, "time": b.Time},
		}
		if b.Host != "" {
			fnd.Evidence["host"] = b.Host
		}
		if b.SizeBytes != nil {
			fnd.Evidence["size_bytes"] = *b.SizeBytes
		}
		if b.Failed {
			fnd.Severity = max(fnd.Severity, model.SeverityWarn)
			fnd.Message += "; it ended with an error"
		}
		if fnd.Severity >= model.SeverityWarn {
			fnd.Hint = "is the backup job scheduled and succeeding? systemctl list-timers, journalctl -u <unit>"
		}
		out = append(out, fnd)
	}
	return out
}

func jobFinding(subject string, j *Job) model.Finding {
	fnd := model.Finding{ID: "backup.job", Subject: subject + " " + j.Unit, Severity: model.SeverityOK}
	switch {
	case j.Error != "":
		fnd.Severity = model.SeverityWarn
		fnd.Message = "cannot read the unit: " + j.Error
	case j.Active == "failed" || (j.Result != "" && j.Result != "success"):
		fnd.Severity = model.SeverityFail
		fnd.Message = fmt.Sprintf("last run failed (result %s, exit status %d)", j.Result, j.ExitStatus)
		fnd.Hint = "journalctl -u " + j.Unit + " -n 50"
	case j.Active == "activating" || j.Active == "active":
		fnd.Message = "running"
	default:
		fnd.Message = "last run succeeded"
	}
	if j.LastRun != "" {
		fnd.Evidence = map[string]any{"last_run": j.LastRun}
	}
	return fnd
}

// age formats seconds as "5h", "3d 4h".
func age(secs float64) string {
	d := time.Duration(secs) * time.Second
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	if h < 48 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd %dh", h/24, h%24)
}
