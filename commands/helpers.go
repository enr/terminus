// This file contains functions that can be reused across multiple commands.
package commands

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/jtopjian/terminus/config"
	"github.com/jtopjian/terminus/facts"
	"github.com/jtopjian/terminus/facts/linux"
	"github.com/jtopjian/terminus/utils"
)

// getLocalFacts handles retrieving facts local to the system terminus is run from.
func getLocalFacts(path string) (*facts.Facts, error) {
	c := config.Config{
		ExternalFactsDir: externalFactsDir,
		Path:             path,
		Debug:            debugFlag,
	}

	switch goos := runtime.GOOS; goos {
	case "linux":
		return linux.GetFacts(c), nil
	default:
		return nil, fmt.Errorf("OS %s is not supported.\n", goos)
	}
}

// parseLocalFacts will take a facts.Facts structure and convert it into a generic interface.
// if a path is given, the facts will be pared down to only the path.
func parseLocalFacts(f *facts.Facts, path string) (interface{}, error) {
	if path != "" {
		var data interface{}
		var value interface{}
		path_pieces := strings.Split(path, ".")

		// Convert the Fact structure into a generic interface{}
		// by first converting it to JSON and then decoding it.
		j, err := json.Marshal(&f.Facts)
		if err != nil {
			utils.ExitWithError(err)
		}

		d := json.NewDecoder(strings.NewReader(string(j)))
		d.UseNumber()
		if err := d.Decode(&data); err != nil {
			utils.ExitWithError(err)
		}

		// Walk through the given path.
		// If there's a result, print it.
		for _, p := range path_pieces {
			i, err := strconv.Atoi(p)
			if err != nil {
				if _, ok := data.(map[string]interface{}); ok {
					value = data.(map[string]interface{})[p]
				}
			} else {
				if _, ok := data.([]interface{}); ok {
					if len(data.([]interface{})) >= i {
						value = data.([]interface{})[i]
					}
				}
			}
			data = value
		}
		return data, nil
	} else {
		data, err := json.MarshalIndent(&f.Facts, " ", "  ")
		if err != nil {
			utils.ExitWithError(err)
		}
		return data, nil
	}
}
