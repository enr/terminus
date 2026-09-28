package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/enr/terminus/internal/buildinfo"
	"github.com/enr/terminus/internal/model"
	"github.com/enr/terminus/internal/output"
	"github.com/enr/terminus/internal/query"
)

// maxQueryBytes limits the body of /facts requests, which only carry a path.
const maxQueryBytes = 4096

// defaultCacheTTL is how long a collected report is reused across requests: collecting runs every
// module (external scripts, backup and postgres connections, TLS dials, ...), so an endpoint
// answering every request unthrottled is an easy way to overload the machine, or the services it
// checks, with a `while true; do curl ...; done`.
const defaultCacheTTL = 5 * time.Second

func newServeCmd(g *globalOptions, stderr io.Writer) *cobra.Command {
	var (
		addr string
		ttl  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve facts and reports over HTTP",
		Long: `Serve facts and reports over HTTP.

  POST /facts    body: optional fact path; returns the facts (or the value at the path)
  GET  /report   the complete report with checks, as JSON

A collection is reused for --cache-ttl across requests (0 disables the cache): serve runs every
enabled module (external scripts, backups, TLS dials, ...) on a miss, so an unauthenticated,
unthrottled endpoint is a way to overload the machine or the services it checks. Bind to a
loopback or private address, or put it behind a reverse proxy that authenticates the caller.`,
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
				Handler:           newHandler(a, log, ttl),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      60 * time.Second,
				IdleTimeout:       120 * time.Second,
			}
			log.Info("listening", "addr", addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&addr, "http", "", "HTTP service address (e.g. ':6060')")
	cmd.Flags().DurationVar(&ttl, "cache-ttl", defaultCacheTTL, "reuse a collection across requests for this long (0: collect on every request)")
	return cmd
}

// cachedCollector runs a.collect at most once every ttl, and never more than once at a time: a
// burst of requests waits for the one collection in flight instead of starting one each.
type cachedCollector struct {
	collect func(context.Context, bool) (*model.Report, error)
	ttl     time.Duration

	mu     sync.Mutex
	at     time.Time
	report *model.Report
	err    error
}

// get returns a report with checks evaluated, collected at most once per ttl. /facts also uses it
// (ignoring the findings): the facts collected are the same regardless of Options.Checks, so
// there is no point running two separate collections a few seconds apart.
func (c *cachedCollector) get(ctx context.Context) (*model.Report, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl > 0 && c.report != nil && time.Since(c.at) < c.ttl {
		return c.report, c.err
	}
	c.report, c.err = c.collect(ctx, true)
	c.at = time.Now()
	return c.report, c.err
}

func newHandler(a *app, log *slog.Logger, ttl time.Duration) http.Handler {
	cache := &cachedCollector{collect: a.collect, ttl: ttl}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /facts", func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(io.LimitReader(req.Body, maxQueryBytes))
		if err != nil {
			httpError(w, log, http.StatusBadRequest, err)
			return
		}
		r, err := cache.get(req.Context())
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
		r, err := cache.get(req.Context())
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
