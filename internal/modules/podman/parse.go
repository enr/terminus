package podman

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func containerIDs(psJSON []byte) ([]string, error) {
	var ps []struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(psJSON, &ps); err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	ids := make([]string, 0, len(ps))
	for _, c := range ps {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

type inspectJSON struct {
	ID           string `json:"Id"`
	Name         string
	ImageName    string
	ImageDigest  string
	Pod          string
	RestartCount int
	State        struct {
		Status     string
		ExitCode   int
		OOMKilled  bool
		StartedAt  string
		FinishedAt string
		Health     *struct {
			Status        string
			FailingStreak int
			Log           []struct {
				ExitCode int
				Output   string
			}
		}
	}
	Config struct {
		Labels      map[string]string
		Healthcheck *struct {
			Test []string
		}
	}
	HostConfig struct {
		RestartPolicy struct{ Name string }
	}
	NetworkSettings struct {
		Ports    map[string][]struct{ HostIP, HostPort string }
		Networks map[string]json.RawMessage
	}
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
}

// zeroTime is how podman prints an unset time.
const zeroTime = "0001-01-01T00:00:00Z"

func parseInspect(b []byte) ([]Container, error) {
	var raw []inspectJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("podman inspect: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{
			Name:          r.Name,
			ID:            shortID(r.ID),
			Image:         r.ImageName,
			ImageDigest:   r.ImageDigest,
			State:         r.State.Status,
			ExitCode:      r.State.ExitCode,
			OOMKilled:     r.State.OOMKilled,
			Pod:           r.Pod,
			SystemdUnit:   r.Config.Labels["PODMAN_SYSTEMD_UNIT"],
			RestartPolicy: r.HostConfig.RestartPolicy.Name,
			Restarts:      r.RestartCount,
		}
		if r.State.StartedAt != zeroTime {
			c.StartedAt = r.State.StartedAt
		}
		if r.State.FinishedAt != zeroTime {
			c.FinishedAt = r.State.FinishedAt
		}
		if hc := r.Config.Healthcheck; hc != nil && len(hc.Test) > 0 && hc.Test[0] != "NONE" {
			c.HasHealthcheck = true
		}
		if h := r.State.Health; h != nil && h.Status != "" {
			c.Health = h.Status
			c.HealthFailingStreak = h.FailingStreak
			if n := len(h.Log); n > 0 {
				c.HealthLastOutput = strings.TrimSpace(h.Log[n-1].Output)
			}
		}
		for port, bindings := range r.NetworkSettings.Ports {
			for _, bd := range bindings {
				host := bd.HostIP
				if host == "" {
					host = "0.0.0.0"
				}
				c.Ports = append(c.Ports, fmt.Sprintf("%s:%s->%s", host, bd.HostPort, port))
			}
		}
		sort.Strings(c.Ports)
		for n := range r.NetworkSettings.Networks {
			c.Networks = append(c.Networks, n)
		}
		sort.Strings(c.Networks)
		for _, mt := range r.Mounts {
			c.Mounts = append(c.Mounts, Mount{Type: mt.Type, Name: mt.Name, Source: mt.Source, Destination: mt.Destination, ReadWrite: mt.RW})
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// attachStats parses podman stats: human readable strings with decimal units ("49.15kB / 16.88GB").
func attachStats(b []byte, cs []Container) error {
	var raw []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		CPUPercent string `json:"cpu_percent"`
		MemUsage   string `json:"mem_usage"`
		PIDs       string `json:"pids"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("podman stats: %w", err)
	}
	for _, r := range raw {
		for i := range cs {
			if cs[i].Name != r.Name && cs[i].ID != shortID(r.ID) {
				continue
			}
			s := &Stats{}
			s.CPUPercent, _ = strconv.ParseFloat(strings.TrimSuffix(r.CPUPercent, "%"), 64)
			if used, limit, ok := strings.Cut(r.MemUsage, "/"); ok {
				s.MemUsageBytes, _ = ParseSize(used)
				s.MemLimitBytes, _ = ParseSize(limit)
			}
			s.PIDs, _ = strconv.Atoi(strings.TrimSpace(r.PIDs))
			cs[i].Stats = s
		}
	}
	return nil
}

// ParseSize parses the sizes printed by podman (go-units): "49.15kB", "1.2GB" (powers of 1000),
// "512MiB" (powers of 1024), "0B".
func ParseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	mult := map[string]float64{
		"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "pb": 1e15,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}[unit]
	if mult == 0 {
		return 0, fmt.Errorf("invalid size unit in %q", s)
	}
	return uint64(math.Round(n * mult)), nil
}

func parseVolumes(b []byte) ([]Volume, error) {
	var raw []struct {
		Name, Driver, Mountpoint string
		Labels                   map[string]string
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("podman volume ls: %w", err)
	}
	out := make([]Volume, 0, len(raw))
	for _, r := range raw {
		v := Volume{Name: r.Name, Driver: r.Driver, Mountpoint: r.Mountpoint}
		if len(r.Labels) > 0 {
			v.Labels = r.Labels
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func parseNetworks(b []byte) ([]Network, error) {
	var raw []struct {
		Name     string `json:"name"`
		Driver   string `json:"driver"`
		Internal bool   `json:"internal"`
		DNS      bool   `json:"dns_enabled"`
		Subnets  []struct {
			Subnet string `json:"subnet"`
		} `json:"subnets"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("podman network ls: %w", err)
	}
	out := make([]Network, 0, len(raw))
	for _, r := range raw {
		n := Network{Name: r.Name, Driver: r.Driver, Internal: r.Internal, DNS: r.DNS}
		for _, s := range r.Subnets {
			n.Subnets = append(n.Subnets, s.Subnet)
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func parseDF(b []byte) ([]StorageUsage, error) {
	var raw []struct {
		Type           string
		Total, Active  int
		RawSize        uint64
		RawReclaimable uint64
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("podman system df: %w", err)
	}
	out := make([]StorageUsage, 0, len(raw))
	for _, r := range raw {
		out = append(out, StorageUsage{Type: r.Type, Total: r.Total, Active: r.Active, SizeBytes: r.RawSize, ReclaimableBytes: r.RawReclaimable})
	}
	return out, nil
}
