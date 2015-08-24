// This is the main/root command for Terminus.
package commands

import (
	"io/ioutil"
	"os"

	"github.com/jtopjian/terminus/utils"
	"github.com/spf13/cobra"
)

var (
	// externalFactsDir specifies where to find external facts
	externalFactsDir string

	// debugFlag enables or disables debug output
	debugFlag bool

	// path defines a dotted-notation path of the fact and fact details
	path string
)

// terminusCmd is the main command. It's the parent to all subcommands
var terminusCmd = &cobra.Command{
	Use:   "terminus",
	Short: "Get facts about a system",
}

// Configure flags and parameters
func init() {
	terminusCmd.PersistentFlags().StringVarP(&externalFactsDir, "external-facts-dir", "e", "/etc/terminus/facts.d", "Path to external facts directory.")
	terminusCmd.PersistentFlags().BoolVarP(&debugFlag, "debug", "d", false, "Debug mode.")
	terminusCmd.Flags().StringVarP(&path, "path", "p", "", "Path to the fact.")

	utils.LogInit(ioutil.Discard, os.Stdout, os.Stdout, os.Stderr)
}

func Execute() {
	// Hack to use "get" as the default command
	// Must specify all subcommands here as well as at the end of this function
	if len(os.Args) == 1 {
		terminusCmd.SetArgs([]string{"get"})
	} else {
		switch os.Args[1] {
		case "-h", "--help", "get", "server", "version":
			break
		default:
			// if the given command did not match a known command, treat it as a path.
			// inject "get" into the list of arguments so the "get" subcommand is run implicitly.
			terminusCmd.SetArgs(append([]string{"get"}, os.Args[1:]...))
		}
	}

	terminusCmd.AddCommand(getFactsCmd)
	terminusCmd.AddCommand(serverCmd)
	terminusCmd.AddCommand(versionCmd)
	terminusCmd.Execute()
}
