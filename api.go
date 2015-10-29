package main

import (
	"io/ioutil"
	"log"
	"net/http"

	"github.com/enr/terminus/config"
)

type httpError struct {
	Error   error
	Message string
	Code    int
}

type httpHandler func(http.ResponseWriter, *http.Request) *httpError

func (fn httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := fn(w, r); err != nil {
		log.Println(err)
		http.Error(w, err.Message, err.Code)
	}
}

func factsHandler(w http.ResponseWriter, r *http.Request) *httpError {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return &httpError{err, "Can't process template string", 500}
	}
	defer r.Body.Close()

	var path string
	if string(body) != "" {
		path = string(body)
	}

	c := config.Config{
		ExternalFactsDir: externalFactsDir,
		Path:             path,
		Debug:            false,
	}

	f := getFacts(c)
	facts, err := parseFacts(f, c)
	if err != nil {
		return &httpError{err, "Error processing facts", 500}
	}

	var output string
	if facts == nil {
		log.Printf(`not found facts for path "%s"`, path)
	} else {
		output, err = formatFacts(facts)
		if err != nil {
			return &httpError{err, "Error processing facts", 500}
		}
	}

	w.Header().Set("Server", "Teminus 1.0.0")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(output))
	return nil
}
