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
	v, _, ok := ResolveSchema(tree, path)
	return v, ok
}

// ResolveSchema is Resolve that also returns the schema path of the value: the map keys walked,
// spelled as in the tree, without the list selections (network.interfaces.eth0.addresses gives
// network.interfaces.addresses). It names what the value is rather than which one it is.
func ResolveSchema(tree any, path string) (any, string, bool) {
	if path == "" {
		return tree, "", true
	}
	segments := strings.Split(path, ".")
	if v, keys, ok := walk(tree, segments); ok {
		return v, strings.Join(keys, "."), true
	}
	if root, ok := tree.(map[string]any); ok {
		if ext, ok := lookup(root, ExternalModule); ok {
			if v, keys, ok := walk(ext, segments); ok {
				return v, strings.Join(append([]string{ExternalModule}, keys...), "."), true
			}
		}
	}
	return nil, "", false
}

func walk(v any, segments []string) (any, []string, bool) {
	var keys []string
	for _, s := range segments {
		switch node := v.(type) {
		case map[string]any:
			k, ok := lookupKey(node, s)
			if !ok {
				return nil, nil, false
			}
			v = node[k]
			keys = append(keys, k)
		case []any:
			if i, err := strconv.Atoi(s); err == nil {
				if i < 0 || i >= len(node) {
					return nil, nil, false
				}
				v = node[i]
				continue
			}
			next, ok := findByKey(node, s)
			if !ok {
				return nil, nil, false
			}
			v = next
		default:
			return nil, nil, false
		}
	}
	return v, keys, true
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

// ItemKey returns the field that names a list element, and its value: eth0 for an interface,
// /srv for a filesystem. ok is false when the element has none. The value selects the element
// in a path unless it contains a dot.
func ItemKey(m map[string]any) (field, value string, ok bool) {
	for _, k := range listKeys {
		if s, isString := m[k].(string); isString && s != "" {
			return k, s, true
		}
	}
	return "", "", false
}

func lookup(m map[string]any, key string) (any, bool) {
	k, ok := lookupKey(m, key)
	if !ok {
		return nil, false
	}
	return m[k], true
}

// lookupKey returns the key of m that matches key: exactly or, failing that, case-insensitively.
func lookupKey(m map[string]any, key string) (string, bool) {
	if _, ok := m[key]; ok {
		return key, true
	}
	for k := range m {
		if strings.EqualFold(k, key) {
			return k, true
		}
	}
	return "", false
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
