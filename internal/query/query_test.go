package query

import (
	"testing"
)

func tree(t *testing.T) any {
	t.Helper()
	type iface struct {
		Name  string
		Addrs []string
	}
	g, err := Generic(map[string]any{
		"system": map[string]any{
			"Hostname": "srv-01",
			"Memory":   map[string]uint64{"Total": 4096},
			"Network":  map[string]any{"eth0": iface{Name: "eth0", Addrs: []string{"10.0.0.1/24", "fe80::1/64"}}},
			"Virtual":  true,
		},
		"network": map[string]any{
			"interfaces":  []map[string]any{{"name": "lo", "mtu": 65536}, {"name": "eth0", "mtu": 1500}},
			"filesystems": []map[string]any{{"mount_point": "/srv", "used_ratio": 0.5}},
		},
		"external": map[string]any{
			"docker": map[string]any{"ServerAPIVersion": "1.16"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestResolve(t *testing.T) {
	tr := tree(t)
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"System.Hostname", "srv-01", true},
		{"system.Hostname", "srv-01", true},
		{"System.hostname", "srv-01", true},
		{"System.Memory.Total", "4096", true},
		{"System.Virtual", "true", true},
		{"System.Network.eth0.Addrs.1", "fe80::1/64", true},
		{"System.Network.eth0.Addrs.2", "", false},
		{"System.Network.eth0.Addrs.-1", "", false},
		{"System.Network.eth0.Addrs.x", "", false},
		{"System.Hostname.x", "", false},
		{"docker.ServerAPIVersion", "1.16", true},
		{"external.docker.ServerAPIVersion", "1.16", true},
		{"nope", "", false},
		{"network.interfaces.eth0.mtu", "1500", true},
		{"network.interfaces.1.mtu", "1500", true},
		{"network.interfaces.wlan0.mtu", "", false},
		{"network.filesystems./srv.used_ratio", "0.5", true},
	}
	for _, c := range cases {
		v, ok := Resolve(tr, c.path)
		if ok != c.ok {
			t.Errorf("%s: found = %v, want %v", c.path, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		got, err := Format(v)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestResolveWholeTreeAndFormatJSON(t *testing.T) {
	tr := tree(t)
	v, ok := Resolve(tr, "")
	if !ok {
		t.Fatal("empty path not resolved")
	}
	if _, isMap := v.(map[string]any); !isMap {
		t.Fatalf("empty path = %T", v)
	}
	v, _ = Resolve(tr, "System.Memory")
	got, _ := Format(v)
	if got != "{\n  \"Total\": 4096\n}" {
		t.Fatalf("format = %q", got)
	}
}

func TestResolveSchema(t *testing.T) {
	tr := tree(t)
	cases := map[string]string{
		"network.interfaces.eth0":  "network.interfaces",
		"Network.Interfaces.0.mtu": "network.interfaces.mtu",
		"network.filesystems./srv": "network.filesystems",
		"docker.ServerAPIVersion":  "external.docker.ServerAPIVersion",
		"system.memory":            "system.Memory",
		"":                         "",
	}
	for path, want := range cases {
		_, got, ok := ResolveSchema(tr, path)
		if !ok || got != want {
			t.Errorf("%q: schema %q (%v), want %q", path, got, ok, want)
		}
	}
	if _, _, ok := ResolveSchema(tr, "network.interfaces.wlan0"); ok {
		t.Error("missing element found")
	}
}

func TestItemKey(t *testing.T) {
	cases := []struct {
		m            map[string]any
		field, value string
		ok           bool
	}{
		{map[string]any{"name": "eth0", "id": "x"}, "name", "eth0", true},
		{map[string]any{"mount_point": "/srv"}, "mount_point", "/srv", true},
		{map[string]any{"name": "", "id": "abc"}, "id", "abc", true},
		{map[string]any{"device": "sda"}, "", "", false},
	}
	for _, c := range cases {
		field, value, ok := ItemKey(c.m)
		if field != c.field || value != c.value || ok != c.ok {
			t.Errorf("%v: %q %q %v", c.m, field, value, ok)
		}
	}
}
