package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/output"
	"github.com/enr/terminus/internal/query"
)

// maxQueryBytes limits the body of /facts requests, which only carry a path.
const maxQueryBytes = 4096

func newServeCmd(g *globalOptions, stderr io.Writer) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve facts and reports over HTTP",
		Long: `Serve facts and reports over HTTP.

  POST /facts    body: optional fact path; returns the facts (or the value at the path)
  GET  /report   the complete report with checks, as JSON`,
		Example: `  terminus serve --http :6060
  curl -d System.Hostname localhost:6060/facts
  curl localhost:6060/report | jq .summary`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if addr == "" {
				return errors.New("--http is required")
			}
			a, err := newApp(g, stderr)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(stderr, nil))
			srv := &http.Server{
				Addr:              addr,
				Handler:           newHandler(a, log),
				ReadHeaderTimeout: 10 * time.Second,
			}
			log.Info("listening", "addr", addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "http", "", "HTTP service address (e.g. ':6060')")
	return cmd
}

func newHandler(a *app, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/facts", func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(io.LimitReader(req.Body, maxQueryBytes))
		if err != nil {
			httpError(w, log, http.StatusBadRequest, err)
			return
		}
		r, err := a.collect(req.Context(), false)
		if err != nil {
			httpError(w, log, http.StatusInternalServerError, err)
			return
		}
		tree, err := query.Generic(r.FactsTree())
		if err != nil {
			httpError(w, log, http.StatusInternalServerError, err)
			return
		}
		v, ok := query.Resolve(tree, string(body))
		if !ok {
			httpError(w, log, http.StatusNotFound, fmt.Errorf("fact %q not found", string(body)))
			return
		}
		s, err := query.Format(v)
		if err != nil {
			httpError(w, log, http.StatusInternalServerError, err)
			return
		}
		setHeaders(w)
		io.WriteString(w, s)
	})
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, req *http.Request) {
		r, err := a.collect(req.Context(), true)
		if err != nil {
			httpError(w, log, http.StatusInternalServerError, err)
			return
		}
		setHeaders(w)
		if err := (output.JSON{}).Render(w, r, output.Options{}); err != nil {
			log.Error("writing report", "err", err)
		}
	})
	return mux
}

func setHeaders(w http.ResponseWriter) {
	v := buildinfo.Version
	if v == "" {
		v = "dev"
	}
	w.Header().Set("Server", "terminus/"+v)
	w.Header().Set("Content-Type", "application/json")
}

func httpError(w http.ResponseWriter, log *slog.Logger, code int, err error) {
	log.Warn("request failed", "code", code, "err", err)
	http.Error(w, err.Error(), code)
}
