// This file contains the command to get/retrieve facts.
package commands

import (
	"encoding/json"
	"fmt"

	"github.com/jtopjian/terminus/utils"
	"github.com/spf13/cobra"
)

// getFactsCmd handles retrieving facts
var getFactsCmd = &cobra.Command{
	Use:   "get",
	Short: "Get a fact or all facts.",
	Run:   getFacts,
}

// Configure flags and parameters
func init() {
	getFactsCmd.PersistentFlags().StringVarP(&path, "path", "p", "", "Path to the fact.")
}

// getFacts does the heavy lifting for getFactsCmd
func getFacts(cmd *cobra.Command, args []string) {
	if path == "" {
		if len(args) > 0 {
			path = args[0]
		}
	}

	f, err := getLocalFacts(path)
	if err != nil {
		utils.ExitWithError(err)
	}

	data, err := parseLocalFacts(f, path)
	if err != nil {
		utils.ExitWithError(err)
	}

	if path != "" {
		// if the data is an []interface{} or map[string]interface{}, convert it to JSON
		if _, ok := data.([]interface{}); ok {
			if j, err := json.Marshal(data); err == nil {
				fmt.Println(string(j))
			}
		} else {
			if _, ok := data.(map[string]interface{}); ok {
				if j, err := json.Marshal(data); err == nil {
					fmt.Println(string(j))
				}
			} else {
				fmt.Println(data)
			}
		}
	} else {
		fmt.Printf("%s\n", data)
	}
}
