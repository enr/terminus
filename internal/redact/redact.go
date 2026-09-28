// Package redact removes credentials from strings before they end up in facts, findings or
// evidence, which are shown, saved to disk and served over HTTP.
package redact

import "regexp"

// credentials matches the userinfo of a URL embedded anywhere in a string: scheme://user:pass@,
// as restic's rest: backend and basic auth in an http(s) URL both allow.
var credentials = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+:[^\s/@]+@`)

// URL masks the userinfo of any URL found in s, keeping the scheme and host so the value still
// says what it points at.
func URL(s string) string {
	return credentials.ReplaceAllString(s, "$1<redacted>@")
}
