// +build !windows

package main

func defaultExternalFacts() string {
	return "/etc/terminus/facts.d"
}
