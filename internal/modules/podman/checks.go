package podman

import (
	"fmt"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
	"github.com/enr/terminus/internal/output"
)

var restartsThreshold = module.Threshold{Warn: 3, Fail: 10, Unit: "restarts"}

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "podman.unhealthy", Description: "container whose healthcheck fails"},
		{ID: "podman.no-healthcheck", Description: "running container without a healthcheck (info)"},
		{ID: "podman.exited", Description: "container of a systemd unit not running, or other container exited with an error"},
		{ID: "podman.oom-killed", Description: "container whose last run was killed by the OOM killer"},
		{ID: "podman.restarts", Description: "restarts of a container done by podman (restart policy)", Threshold: &restartsThreshold},
		{ID: "podman.volume-unused", Description: "volume that no container mounts (info): a typo or a quadlet volume unit not used"},
		{ID: "podman.storage", Description: "reclaimable space of images, containers and volumes (info)"},
		{ID: "podman.scope", Description: "podman could not be inspected for root or a user"},
	}
}

// Check implements module.Checker.
func (*Module) Check(env *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	restarts := env.Threshold("podman.restarts", restartsThreshold)
	var out []model.Finding
	for _, s := range f.Scopes {
		if s.Skipped != "" {
			out = append(out, model.Finding{ID: "podman.scope", Subject: s.Name, Severity: model.SeverityInfo, Message: "not inspected: " + s.Skipped})
			continue
		}
		if len(s.Errors) > 0 {
			out = append(out, model.Finding{
				ID: "podman.scope", Subject: s.Name, Severity: model.SeverityWarn,
				Message: "partial data: " + strings.Join(s.Errors, "; "),
			})
		}
		for _, c := range s.Containers {
			out = append(out, containerFindings(s, c, restarts)...)
		}
		for _, v := range s.Volumes {
			if len(v.UsedBy) == 0 {
				out = append(out, model.Finding{
					ID: "podman.volume-unused", Subject: s.Name + ":" + v.Name, Severity: model.SeverityInfo,
					Message: "no container mounts this volume",
					Hint:    "a quadlet .volume unit is used only when the .container refers to it as name.volume",
				})
			}
		}
		var reclaim uint64
		for _, u := range s.Storage {
			reclaim += u.ReclaimableBytes
		}
		if reclaim > 0 {
			out = append(out, model.Finding{
				ID: "podman.storage", Subject: s.Name, Severity: model.SeverityInfo,
				Message:  fmt.Sprintf("%s reclaimable (unused images, stopped containers, unused volumes)", output.HumanBytes(reclaim)),
				Evidence: map[string]any{"reclaimable_bytes": reclaim},
				Hint:     "podman system df -v shows what; podman image prune removes dangling images",
			})
		}
	}
	return out
}

func containerFindings(s Scope, c Container, restarts module.Threshold) []model.Finding {
	subj := s.Name + ":" + c.Name
	var out []model.Finding
	switch {
	case c.Health == "unhealthy":
		f := model.Finding{
			ID: "podman.unhealthy", Subject: subj, Severity: model.SeverityFail,
			Message: fmt.Sprintf("healthcheck failing %d times in a row", c.HealthFailingStreak),
			Hint:    "podman healthcheck run " + c.Name + "; podman logs " + c.Name,
		}
		if c.HealthLastOutput != "" {
			f.Evidence = map[string]any{"last_output": c.HealthLastOutput}
		}
		out = append(out, f)
	case c.State == "running" && !c.HasHealthcheck:
		out = append(out, model.Finding{
			ID: "podman.no-healthcheck", Subject: subj, Severity: model.SeverityInfo,
			Message: "no healthcheck: podman cannot tell whether the service answers",
			Hint:    "HealthCmd= in the quadlet file (podman 4.9: check it with quadlet -dryrun first)",
		})
	}

	if c.State != "running" && c.State != "created" {
		switch {
		case c.SystemdUnit != "":
			out = append(out, model.Finding{
				ID: "podman.exited", Subject: subj, Severity: model.SeverityFail,
				Message: fmt.Sprintf("%s (exit code %d), but it belongs to %s", c.State, c.ExitCode, c.SystemdUnit),
				Hint:    "the unit should run it: check systemctl status " + c.SystemdUnit,
			})
		case c.ExitCode != 0:
			out = append(out, model.Finding{
				ID: "podman.exited", Subject: subj, Severity: model.SeverityWarn,
				Message: fmt.Sprintf("exited with code %d", c.ExitCode),
				Hint:    "podman logs " + c.Name + "; podman rm " + c.Name + " if it is not needed",
			})
		}
	}
	if c.OOMKilled {
		out = append(out, model.Finding{
			ID: "podman.oom-killed", Subject: subj, Severity: model.SeverityFail,
			Message: "killed by the OOM killer",
			Hint:    "raise the memory limit (MemoryMax= in the unit, --memory) or reduce the memory use",
		})
	}
	if c.Restarts > 0 {
		out = append(out, model.Finding{
			ID: "podman.restarts", Subject: subj, Severity: restarts.Grade(float64(c.Restarts)),
			Message:  fmt.Sprintf("restarted %d times by podman (policy %s)", c.Restarts, c.RestartPolicy),
			Evidence: map[string]any{"restarts": c.Restarts},
		})
	}
	return out
}
