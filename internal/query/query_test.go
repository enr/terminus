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
