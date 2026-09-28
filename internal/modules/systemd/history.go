package systemd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/enr/terminus/internal/runner"
)

// Journal message IDs logged by systemd (see /usr/lib/systemd/catalog/systemd.catalog).
const (
	msgOutOfMemory      = "fe6faa94e7774663a0da52717891d8ef" // a process of the unit was killed by the OOM killer
	msgUnitFailed       = "d9b373ed55a64feb8242e02dbe79a49c" // UNIT_RESULT
	msgRestartScheduled = "5eb03494b6584870a536b337290809b3"
	msgProcessExited    = "98e322203f7a4ed290d09fe03c09fe15" // EXIT_CODE, EXIT_STATUS
)

// History summarizes what the journal says about a unit in the history window.
type History struct {
	OOMKills    int    `json:"oom_kills"`
	LastOOMKill string `json:"last_oom_kill,omitempty"`
	Restarts    int    `json:"restarts"`
	// Failures counts the failures by result (oom-kill, exit-code, signal, timeout, ...).
	Failures map[string]int `json:"failures,omitempty"`
	// Exits counts the main process exits by code and status: "exited/143", "killed/9".
	Exits map[string]int `json:"exits,omitempty"`
}

type journalEntry struct {
	MessageID  string `json:"MESSAGE_ID"`
	Unit       string `json:"UNIT"`
	UserUnit   string `json:"USER_UNIT"`
	UID        string `json:"_UID"`
	Result     string `json:"UNIT_RESULT"`
	ExitCode   string `json:"EXIT_CODE"`
	ExitStatus string `json:"EXIT_STATUS"`
	Realtime   string `json:"__REALTIME_TIMESTAMP"`
}

// attachHistory reads the systemd messages of the window and adds them to the units. Root reads
// the system and all the user journals; other users only what they can access.
func (m *Module) attachHistory(ctx context.Context, r runner.Runner, f *Facts) error {
	args := []string{
		"--no-pager", "-o", "json",
		fmt.Sprintf("--since=-%ds", int(m.history.Seconds())),
		"--output-fields=MESSAGE_ID,UNIT,USER_UNIT,_UID,UNIT_RESULT,EXIT_CODE,EXIT_STATUS",
		"MESSAGE_ID=" + msgOutOfMemory,
		"MESSAGE_ID=" + msgUnitFailed,
		"MESSAGE_ID=" + msgRestartScheduled,
		"MESSAGE_ID=" + msgProcessExited,
	}
	userByUID := map[string]string{}
	for _, u := range f.Users {
		userByUID[strconv.FormatUint(uint64(u.UID), 10)] = u.Name
	}
	histories := map[string]*History{} // "manager\x00unit"
	get := func(manager, unit string) *History {
		k := manager + "\x00" + unit
		h, ok := histories[k]
		if !ok {
			h = &History{}
			histories[k] = h
		}
		return h
	}

	res, err := r.Stream(ctx, runner.Cmd{Name: "journalctl", Args: args, Timeout: commandTimeout}, func(line []byte) error {
		var e journalEntry
		if json.Unmarshal(line, &e) != nil {
			return nil // binary or unexpected fields: skip the entry
		}
		manager, unit := "system", e.Unit
		if e.UserUnit != "" {
			name, ok := userByUID[e.UID]
			if !ok {
				return nil // a user that is not selected
			}
			manager, unit = "user:"+name, e.UserUnit
		}
		if unit == "" {
			return nil
		}
		h := get(manager, unit)
		switch e.MessageID {
		case msgOutOfMemory:
			h.OOMKills++
			if ts := realtime(e.Realtime); ts != "" {
				h.LastOOMKill = ts
			}
		case msgUnitFailed:
			if h.Failures == nil {
				h.Failures = map[string]int{}
			}
			h.Failures[e.Result]++
		case msgRestartScheduled:
			h.Restarts++
		case msgProcessExited:
			if h.Exits == nil {
				h.Exits = map[string]int{}
			}
			h.Exits[e.ExitCode+"/"+e.ExitStatus]++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	// journalctl exits with 1 when the journal has no entry for the filters.
	if res.ExitCode != 0 && len(strings.TrimSpace(string(res.Stderr))) > 0 {
		return fmt.Errorf("journalctl: exit code %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}

	for k, h := range histories {
		manager, unit, _ := strings.Cut(k, "\x00")
		// A unit that failed with oom-kill without the OOM message (older systemd) counts too.
		if h.OOMKills == 0 && h.Failures["oom-kill"] > 0 {
			h.OOMKills = h.Failures["oom-kill"]
		}
		for i := range f.Managers {
			mg := &f.Managers[i]
			if mg.Name != manager {
				continue
			}
			found := false
			for j := range mg.Units {
				if mg.Units[j].Name == unit {
					mg.Units[j].History, found = h, true
				}
			}
			// Stopped units are not listed in detail, but their history matters (it crashed).
			if !found && mg.Skipped == "" && mg.Error == "" {
				mg.Units = append(mg.Units, Unit{Name: unit, History: h})
			}
		}
	}
	for i := range f.Managers {
		units := f.Managers[i].Units
		sort.Slice(units, func(a, b int) bool { return units[a].Name < units[b].Name })
	}
	return nil
}

// realtime converts __REALTIME_TIMESTAMP (microseconds since the epoch) to RFC 3339.
func realtime(us string) string {
	n, err := strconv.ParseInt(us, 10, 64)
	if err != nil {
		return ""
	}
	return time.UnixMicro(n).UTC().Format(time.RFC3339)
}

// describeExit explains an exit as the journal records it.
func describeExit(key string) string {
	code, status, _ := strings.Cut(key, "/")
	switch {
	case code == "exited" && status == "137":
		return "137 (SIGKILL: OOM or kill -9)"
	case code == "exited" && status == "143":
		return "143 (SIGTERM: stop or deploy)"
	case code == "killed" && status == "9":
		return "SIGKILL"
	case code == "killed" && status == "15":
		return "SIGTERM (stop or deploy)"
	case code == "exited":
		return "exit " + status
	default:
		return code + " " + status
	}
}
