package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/diff"
	"github.com/enr/terminus/internal/model"
)

func newDiffCmd(stdout io.Writer) *cobra.Command {
	var (
		format          string
		facts           bool
		ignore          []string
		noDefaultIgnore bool
	)
	cmd := &cobra.Command{
		Use:   "diff BEFORE.json AFTER.json",
		Short: "Compare two JSON reports",
		Long: `Compare two JSON reports (terminus check -o json, terminus report -o json): new, resolved
and changed findings and, with --facts, the facts that changed.

Values that change at every run (time, uptime, counters, current usage) are left out of the facts;
--ignore adds more patterns ("*" matches any text), --no-default-ignore shows everything.

Exit code: 0 nothing got worse, 1 new warnings, 2 new failures, 3 terminus error.`,
		Example: `  terminus report -o json --output-file before.json
  # deploy ...
  terminus report -o json --output-file after.json
  terminus diff before.json after.json --facts`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			before, err := diff.Load(args[0])
			if err != nil {
				return err
			}
			after, err := diff.Load(args[1])
			if err != nil {
				return err
			}
			patterns := ignore
			if !noDefaultIgnore {
				patterns = append(append([]string{}, diff.DefaultIgnore...), ignore...)
			}
			res := diff.Compare(before, after, facts, patterns)
			if err := diff.Render(stdout, res, format); err != nil {
				return err
			}
			if code := res.ExitCode(); code != model.ExitOK {
				return &exitError{code: code}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&format, "output", "o", "text", "output format: text, json, markdown")
	f.BoolVar(&facts, "facts", false, "compare the facts too")
	f.StringSliceVar(&ignore, "ignore", nil, "fact paths to leave out (globs, e.g. '*.mtu')")
	f.BoolVar(&noDefaultIgnore, "no-default-ignore", false, "do not leave out the values that change at every run")
	return cmd
}
