package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/enr/terminus/lib/config"
	"github.com/enr/terminus/lib/facts"
)

func getFacts(c config.Config) *facts.Facts {
	f := facts.New()
	switch goos := runtime.GOOS; goos {
	case "linux":
		f = facts.GetFacts(c)
	case "windows", "darwin":
		log.Printf("OS %s not yet fully supported.\n", goos)
		f = facts.GetFacts(c)
	default:
		errorAndExit(fmt.Errorf("OS %s is not supported.\n", goos))
	}
	return f
}

func parseFacts(f *facts.Facts, c config.Config) (interface{}, error) {
	var data interface{}
	var value interface{}
	path_pieces := strings.Split(c.Path, ".")

	// Convert the Fact structure into a generic interface{}
	// by first converting it to JSON and then decoding it.
	j, err := json.Marshal(&f.Facts)
	if err != nil {
		return nil, err
	}

	d := json.NewDecoder(strings.NewReader(string(j)))
	d.UseNumber()
	if err := d.Decode(&data); err != nil {
		return nil, err
	}

	// Walk through the given path.
	// If there's a result, print it.
	if len(path_pieces) > 1 {
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
	}

	return data, nil
}

func formatFacts(facts interface{}) (string, error) {
	if _, ok := facts.([]interface{}); ok {
		if j, err := json.MarshalIndent(facts, " ", " "); err != nil {
			return "", err
		} else {
			return string(j), nil
		}
	} else {
		if _, ok := facts.(map[string]interface{}); ok {
			if j, err := json.MarshalIndent(facts, " ", " "); err != nil {
				return "", err
			} else {
				return string(j), nil
			}
		} else {
			return fmt.Sprintf("%s", facts), nil
		}
	}
}

func errorAndExit(err error) {
	log.Fatal(err)
	os.Exit(1)
}
