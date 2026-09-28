// Package query resolves dotted paths (System.Network.Interfaces.eth0.IP4Addresses.0.IP)
// against a facts tree.
package query

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ExternalModule is the module searched when the first path segment is not a module name,
// so that queries on external facts (docker.ServerAPIVersion) keep working as in terminus v1.
const ExternalModule = "external"

// Generic converts any value into the generic JSON representation (map[string]any, []any,
// json.Number, string, bool, nil), which is what Resolve walks.
func Generic(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Resolve walks the path in a generic tree. An empty path returns the whole tree.
// Map keys match exactly or, failing that, case-insensitively; numeric segments index lists,
// other segments select the list element with that name (see listKeys).
func Resolve(tree any, path string) (any, bool) {
	if path == "" {
		return tree, true
	}
	segments := strings.Split(path, ".")
	if v, ok := walk(tree, segments); ok {
		return v, true
	}
	if root, ok := tree.(map[string]any); ok {
		if ext, ok := lookup(root, ExternalModule); ok {
			return walk(ext, segments)
		}
	}
	return nil, false
}

func walk(v any, segments []string) (any, bool) {
	for _, s := range segments {
		switch node := v.(type) {
		case map[string]any:
			next, ok := lookup(node, s)
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			if i, err := strconv.Atoi(s); err == nil {
				if i < 0 || i >= len(node) {
					return nil, false
				}
				v = node[i]
				continue
			}
			next, ok := findByKey(node, s)
			if !ok {
				return nil, false
			}
			v = next
		default:
			return nil, false
		}
	}
	return v, true
}

// listKeys are the fields that identify an element of a list: a non-numeric path segment selects
// the element whose key field has that value (network.interfaces.eth0, storage.filesystems./srv).
var listKeys = []string{"name", "mount_point", "id"}

func findByKey(list []any, value string) (any, bool) {
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range listKeys {
			if s, ok := m[k].(string); ok && s == value {
				return m, true
			}
		}
	}
	return nil, false
}

func lookup(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// Format renders a resolved value: maps and lists as indented JSON, scalars as plain text.
func Format(v any) (string, error) {
	switch v.(type) {
	case map[string]any, []any:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	case nil:
		return "", nil
	default:
		return fmt.Sprint(v), nil
	}
}
