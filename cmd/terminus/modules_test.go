package main

import (
	"bytes"
	"reflect"
	"testing"
)

func TestWrap(t *testing.T) {
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"short", 10, []string{"short"}},
		{"one two three four", 9, []string{"one two", "three", "four"}},
		{"abcdefghij kl", 4, []string{"abcd", "efgh", "ij", "kl"}},
		{"", 5, []string{""}},
	}
	for _, c := range cases {
		if got := wrap(c.text, c.width); !reflect.DeepEqual(got, c.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
}

func TestPrintTableWidth(t *testing.T) {
	header := []string{"NAME", "KIND", "DESCRIPTION"}
	rows := [][]string{
		{"cpu", "core", "processors, load average, CPU pressure"},
		{"backup", "optional", "backups: latest backup, age, result of the backup job"},
	}
	cases := map[int]string{
		// No limit, or wide enough: one line per row.
		0: `NAME    KIND      DESCRIPTION
cpu     core      processors, load average, CPU pressure
backup  optional  backups: latest backup, age, result of the backup job
`,
		// The description wraps beside the other columns.
		50: `NAME    KIND      DESCRIPTION
cpu     core      processors, load average, CPU
                  pressure
backup  optional  backups: latest backup, age,
                  result of the backup job
`,
		// Too little room beside them: below the row.
		40: `NAME    KIND
cpu     core
    processors, load average, CPU
    pressure
backup  optional
    backups: latest backup, age, result
    of the backup job
`,
	}
	for width, want := range cases {
		var buf bytes.Buffer
		printTableWidth(&buf, width, header, rows)
		if buf.String() != want {
			t.Errorf("width %d:\n%s\nwant:\n%s", width, buf.String(), want)
		}
	}
}
