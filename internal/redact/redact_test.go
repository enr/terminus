package redact

import "testing"

func TestURL(t *testing.T) {
	cases := map[string]string{
		"rest:https://backup:s3cr3t@nas:8000/repo": "rest:https://<redacted>@nas:8000/repo",
		"https://user:pass@example.org/health":     "https://<redacted>@example.org/health",
		"/srv/backup/restic":                       "/srv/backup/restic",
		"sftp:backup@nas:/srv/restic":              "sftp:backup@nas:/srv/restic",
		"s3:https://minio.example.com/bucket":      "s3:https://minio.example.com/bucket",
	}
	for in, want := range cases {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}
