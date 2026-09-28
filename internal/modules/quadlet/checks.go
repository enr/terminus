package quadlet

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/module"
)

// Checks implements module.Checker.
func (*Module) Checks() []module.CheckInfo {
	return []module.CheckInfo{
		{ID: "quadlet.dryrun", Description: "the quadlet generator of the machine rejects a file or fails"},
		{ID: "quadlet.volume-not-used", Description: "a .container mounts a volume by name while a .volume unit for it exists: the unit is not used"},
		{ID: "quadlet.network-not-used", Description: "a .container joins a network by name while a .network unit for it exists: the unit is not used"},
		{ID: "quadlet.unit-not-loaded", Description: "a quadlet file whose generated unit systemd does not know (daemon-reload missing, generator error)"},
		{ID: "quadlet.unit-never-active", Description: "a .volume/.network unit that is not active: no container requires it"},
		{ID: "quadlet.changed-since-start", Description: "a quadlet file changed after its service started: the change is not in effect"},
		{ID: "quadlet.volume-owner", Description: "a volume whose real owner differs from User=/Group= of its .volume file"},
		{ID: "quadlet.scope", Description: "quadlet files could not be inspected for root or a user"},
	}
}

// Check implements module.Checker.
func (*Module) Check(_ *module.Env, facts any) []model.Finding {
	f, ok := facts.(*Facts)
	if !ok {
		return nil
	}
	var out []model.Finding
	for _, s := range f.Scopes {
		if s.Skipped != "" {
			out = append(out, model.Finding{ID: "quadlet.scope", Subject: s.Name, Severity: model.SeverityInfo, Message: "not inspected: " + s.Skipped})
			continue
		}
		if len(s.Errors) > 0 {
			out = append(out, model.Finding{ID: "quadlet.scope", Subject: s.Name, Severity: model.SeverityWarn, Message: "partial data: " + strings.Join(s.Errors, "; ")})
		}
		out = append(out, scopeFindings(f.Binary, s)...)
	}
	return out
}

func scopeFindings(bin string, s Scope) []model.Finding {
	var out []model.Finding
	byName := map[string]File{}
	for _, fl := range s.Files {
		byName[fl.Name] = fl
	}
	subj := func(fl File) string { return s.Name + ":" + fl.Name }
	dryRunHint := fmt.Sprintf("QUADLET_UNIT_DIRS=%s %s -dryrun", strings.Join(s.Dirs, ":"), bin)
	if s.Rootless {
		dryRunHint += " -user"
	}

	if s.DryRun == "failed" && len(s.Diagnostics) > 0 {
		out = append(out, model.Finding{
			ID: "quadlet.dryrun", Subject: s.Name, Severity: model.SeverityFail,
			Message: "the quadlet generator failed: " + strings.Join(s.Diagnostics, "; "), Hint: dryRunHint,
		})
	}
	for _, fl := range s.Files {
		if len(fl.Diagnostics) > 0 {
			out = append(out, model.Finding{
				ID: "quadlet.dryrun", Subject: subj(fl), Severity: model.SeverityFail,
				Message: strings.Join(fl.Diagnostics, "; "),
				Hint:    "the generator skips this file, so its unit does not exist: " + dryRunHint,
			})
		}

		if fl.Kind == "container" {
			out = append(out, referenceFindings(s, fl, byName)...)
		}

		if st := fl.State; st != nil {
			switch {
			case st.Load == "not-found" && len(fl.Diagnostics) == 0:
				out = append(out, model.Finding{
					ID: "quadlet.unit-not-loaded", Subject: subj(fl), Severity: model.SeverityFail,
					Message: fl.Service + " is not loaded in systemd",
					Hint:    daemonReload(s),
				})
			case (fl.Kind == "volume" || fl.Kind == "network") && st.Active == "inactive":
				out = append(out, model.Finding{
					ID: "quadlet.unit-never-active", Subject: subj(fl), Severity: model.SeverityWarn,
					Message: fmt.Sprintf("%s is loaded but inactive (dead): no running container requires it", fl.Service),
					Hint:    fmt.Sprintf("containers must refer to it as %s (with the suffix); once used it is active (exited)", fl.Name),
				})
			case st.Active == "active" && !st.ActiveSince.IsZero() && fl.ModTime.After(st.ActiveSince):
				out = append(out, model.Finding{
					ID: "quadlet.changed-since-start", Subject: subj(fl), Severity: model.SeverityWarn,
					Message:  fmt.Sprintf("changed at %s, after %s started (%s): the change is not in effect", fl.ModTime.Format("2006-01-02 15:04"), fl.Service, st.ActiveSince.Format("2006-01-02 15:04")),
					Evidence: map[string]any{"mod_time": fl.ModTime, "active_since": st.ActiveSince},
					Hint:     daemonReload(s) + " && " + systemctl(s) + " restart " + fl.Service,
				})
			}
		}

		if fl.Kind == "volume" {
			if f := ownerFinding(s, fl); f != nil {
				out = append(out, *f)
			}
		}
	}
	return out
}

// referenceFindings reports the volumes and networks a container names directly while a quadlet
// unit for them exists: podman then uses another object, and the unit is not started.
func referenceFindings(s Scope, fl File, byName map[string]File) []model.Finding {
	var out []model.Finding
	check := func(id, kind string, values []string, used func(string) bool) {
		for _, v := range values {
			src, rest, _ := strings.Cut(v, ":")
			if src == "" || strings.Contains(src, "/") || strings.HasSuffix(src, "."+kind) {
				continue
			}
			unit, ok := byName[src+"."+kind]
			if !ok {
				continue
			}
			f := model.Finding{
				ID: id, Subject: s.Name + ":" + fl.Name, Severity: model.SeverityFail,
				Message: fmt.Sprintf("uses the %s %q directly: %s (%s %q) is not used", kind, src, unit.Name, kind, unit.ObjectName),
				Evidence: map[string]any{
					"declared":            v,
					"unit":                unit.Name,
					"unit_" + kind:        unit.ObjectName,
					"used_by_podman":      src,
					"confirmed_by_dryrun": fl.Generated != nil && used(src),
				},
				Hint: fmt.Sprintf("write Network=%s.network (then %s requires %s and joins %q)", src, fl.Service, unit.Service, unit.ObjectName),
			}
			if kind == "volume" && rest != "" {
				f.Hint = fmt.Sprintf("write Volume=%s.volume:%s (then %s requires %s and mounts %q)", src, rest, fl.Service, unit.Service, unit.ObjectName)
			}
			if _, a := s.Volumes[src]; kind == "volume" && a {
				if _, b := s.Volumes[unit.ObjectName]; b {
					f.Message += fmt.Sprintf("; both volumes %q and %q exist", src, unit.ObjectName)
				}
			}
			out = append(out, f)
		}
	}
	check("quadlet.volume-not-used", "volume", fl.Volumes, func(src string) bool { return contains(fl.Generated.Volumes, src) })
	check("quadlet.network-not-used", "network", fl.Networks, func(src string) bool { return contains(fl.Generated.Networks, src) })
	return out
}

// ownerFinding compares User=/Group= of a .volume file with the real owner of the volume.
func ownerFinding(s Scope, fl File) *model.Finding {
	if fl.User == "" && fl.Group == "" {
		return nil
	}
	v, ok := s.Volumes[fl.ObjectName]
	if !ok || v.OwnerUID == nil {
		return nil
	}
	var diffs []string
	if n, err := strconv.ParseInt(fl.User, 10, 64); err == nil && n != *v.OwnerUID {
		diffs = append(diffs, fmt.Sprintf("uid %d instead of User=%s", *v.OwnerUID, fl.User))
	}
	if n, err := strconv.ParseInt(fl.Group, 10, 64); err == nil && n != *v.OwnerGID {
		diffs = append(diffs, fmt.Sprintf("gid %d instead of Group=%s", *v.OwnerGID, fl.Group))
	}
	if len(diffs) == 0 {
		return nil
	}
	f := &model.Finding{
		ID: "quadlet.volume-owner", Subject: s.Name + ":" + fl.Name, Severity: model.SeverityWarn,
		Message:  fmt.Sprintf("volume %q is owned by %s (as seen in the container)", fl.ObjectName, strings.Join(diffs, ", ")),
		Evidence: map[string]any{"mountpoint": v.Mountpoint, "host_owner_uid": v.HostOwnerUID},
		Hint:     "the ownership comes from the copy-up of the first mount; User=/Group= without Device= may not apply",
	}
	if fl.Device == "" {
		f.Message += "; User=/Group= are set without Device="
	}
	return f
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func systemctl(s Scope) string {
	if s.Rootless {
		return "systemctl --user -M " + s.Name + "@"
	}
	return "systemctl"
}

func daemonReload(s Scope) string { return systemctl(s) + " daemon-reload" }
