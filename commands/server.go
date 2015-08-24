// This file contains the command to enable Terminus to act as an HTTP server.
package commands

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"text/template"

	"github.com/jtopjian/terminus/utils"
	"github.com/spf13/cobra"
)

// serverCmd runs terminus in server mode and will report facts back via http requests
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Runs terminus as an HTTP server to report facts remotely.",
	Run:   server,
}

var (
	// listen is the address/port that terminus should listen on
	listen string
)

// Configure flags and parameters
func init() {
	serverCmd.Flags().StringVarP(&listen, "http", "", ":8080", "address/port to listen on.")
	serverCmd.Flags().StringVarP(&listen, "listen", "", ":8080", "address/port to listen on.")
}

type httpError struct {
	Error   error
	Message string
	Code    int
}

type httpHandler func(http.ResponseWriter, *http.Request) *httpError

func server(cmd *cobra.Command, args []string) {
	http.Handle("/facts", httpHandler(factsHandler))
	utils.ExitWithError(http.ListenAndServe(listen, nil))
}

func (fn httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := fn(w, r); err != nil {
		log.Println(err)
		http.Error(w, err.Message, err.Code)
	}
}

func factsHandler(w http.ResponseWriter, r *http.Request) *httpError {
	f, err := getLocalFacts("")
	if err != nil {
		utils.ExitWithError(err)
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return &httpError{err, "Can't process template string", 500}
	}
	defer r.Body.Close()

	if string(body) != "" {
		tmpl, err := template.New("format").Parse(string(body))
		if err != nil {
			return &httpError{err, "Can't process template string", 500}
		}
		err = tmpl.Execute(w, &f.Facts)
		if err != nil {
			return &httpError{err, "Can't process template string", 500}
		}
		return nil
	}

	data, err := json.MarshalIndent(&f.Facts, " ", "  ")
	if err != nil {
		return &httpError{err, "Error processing facts", 500}
	}
	w.Header().Set("Server", "Teminus 1.0.0")
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
	return nil
}
