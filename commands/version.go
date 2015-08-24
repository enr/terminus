// This file contains the command to return the versiof of Terminus.
package commands

import (
	"fmt"

	"github.com/jtopjian/terminus/version"
	"github.com/spf13/cobra"
)

// versionCmd prints the version.
// The version is defined in version/version.go
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version.",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Terminus %s\n", version.Version)
	},
}
