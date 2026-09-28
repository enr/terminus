package quadlet

import (
	"strings"
)

// UnitFile is a parsed systemd/quadlet unit file: sections with repeatable keys, in order.
type UnitFile struct {
	sections map[string][]kv
}

type kv struct{ key, value string }

// ParseUnit parses the systemd unit file syntax: [Section], Key=Value, comments (# ;), and
// lines continued with a trailing backslash.
func ParseUnit(lines []string) UnitFile {
	u := UnitFile{sections: map[string][]kv{}}
	section := ""
	var pending string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if pending != "" {
			line = pending + " " + line
			pending = ""
		}
		if strings.HasSuffix(line, `\`) {
			pending = strings.TrimSpace(strings.TrimSuffix(line, `\`))
			continue
		}
		switch {
		case line == "", strings.HasPrefix(line, "#"), strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
		default:
			k, v, ok := strings.Cut(line, "=")
			if ok {
				u.sections[section] = append(u.sections[section], kv{strings.TrimSpace(k), strings.TrimSpace(v)})
			}
		}
	}
	if pending != "" && section != "" {
		if k, v, ok := strings.Cut(pending, "="); ok {
			u.sections[section] = append(u.sections[section], kv{strings.TrimSpace(k), strings.TrimSpace(v)})
		}
	}
	return u
}

// Values returns every value of a key in a section. An empty assignment resets the list, as
// systemd does.
func (u UnitFile) Values(section, key string) []string {
	var out []string
	for _, e := range u.sections[section] {
		if e.key != key {
			continue
		}
		if e.value == "" {
			out = nil
			continue
		}
		out = append(out, e.value)
	}
	return out
}

// Value returns the last value of a key, or "".
func (u UnitFile) Value(section, key string) string {
	v := u.Values(section, key)
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// Has tells whether a key is set in a section.
func (u UnitFile) Has(section, key string) bool {
	return len(u.Values(section, key)) > 0
}
