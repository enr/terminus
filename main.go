// Copyright (c) 2015 Joe Topjian. All rights reserved.
// Copyright (c) 2014 Kelsey Hightower. All rights reserved.
// Use of this source code is governed by the Apache License, Version 2.0
// that can be found in the LICENSE file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/jtopjian/terminus/config"
	"github.com/jtopjian/terminus/facts"
	"github.com/jtopjian/terminus/facts/linux"
)

var (
	externalFactsDir string
	httpAddr         string
	printVersion     bool
	debug            bool
	path             string
	data             interface{}
	value            interface{}
	path_pieces      []string
)

func init() {
	log.SetFlags(0)
	flag.StringVar(&externalFactsDir, "external-facts-dir", defaultExternalFacts, "Path to external facts directory.")
	flag.StringVar(&httpAddr, "http", "", "HTTP service address (e.g., ':6060')")
	flag.BoolVar(&printVersion, "version", false, "print version and exit")
	flag.BoolVar(&debug, "debug", false, "print errors to stderr instead of ignoring them")
}

var defaultExternalFacts = "/etc/terminus/facts.d"

func main() {
	flag.Parse()

	if printVersion {
		fmt.Printf("terminus %s\n", Version)
		os.Exit(0)
	}

	if httpAddr != "" {
		http.Handle("/facts", httpHandler(factsHandler))
		log.Fatal(http.ListenAndServe(httpAddr, nil))
	}

	// If there are arguments left over, use the first argument as a fact query.
	if len(flag.Args()) > 0 {
		path = flag.Args()[0]
		path_pieces = strings.Split(path, ".")
	}

	f := getFacts()

	if path != "" {
		// Convert the Fact structure into a generic interface{}
		// by first converting it to JSON and then decoding it.
		j, err := json.Marshal(&f.Facts)
		if err != nil {
			log.Fatal(err)
		}

		d := json.NewDecoder(strings.NewReader(string(j)))
		d.UseNumber()
		if err := d.Decode(&data); err != nil {
			log.Fatal(err)
			os.Exit(1)
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
		if value != nil {
			// if the value is an []interface{} or map[string]interface{}, convert it to JSON
			if _, ok := value.([]interface{}); ok {
				if j, err := json.Marshal(value); err == nil {
					fmt.Println(string(j))
				}
			} else {
				if _, ok := value.(map[string]interface{}); ok {
					if j, err := json.Marshal(value); err == nil {
						fmt.Println(string(j))
					}
				} else {
					fmt.Println(value)
				}
			}
		}
		os.Exit(0)
	}

	data, err := json.MarshalIndent(&f.Facts, " ", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", data)
}

func getFacts() *facts.Facts {
	c := config.Config{
		ExternalFactsDir: externalFactsDir,
		Path:             path,
		Debug:            debug,
	}
	f := facts.New()
	switch goos := runtime.GOOS; goos {
	case "linux":
		f = linux.GetFacts(c)
	default:
		fmt.Printf("OS %s is not supported.\n", goos)
		os.Exit(1)
	}
	return f
}
