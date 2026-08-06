package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/satoricorp/tennis"
)

// maxRequestBody bounds what a handler will read. A write of many documents is
// legitimately large; unbounded is not — without a cap a runaway client feeds
// the JSON decoder forever.
const maxRequestBody = 32 << 20

// The HTTP API exists so tennis is usable from languages that will never have
// a Go SDK. One binary speaks JSON on a local port and a client in any language
// is a hundred lines, which beats maintaining five real SDKs.
//
// It binds to loopback by default and has no authentication, because it is an
// interface to a local file rather than a service. --addr will happily bind
// elsewhere; do not, unless you have put something in front of it.
func cmdServe(args []string) error {
	fs_ := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	addr := fs_.String("addr", "127.0.0.1:8817", "listen address (loopback only unless you know why)")
	fs_.Parse(args)

	db, err := open(*dbPath, false)
	if err != nil {
		return err
	}
	defer db.Close()

	if host, _, err := net.SplitHostPort(*addr); err == nil && !isLoopback(host) {
		fmt.Fprintf(os.Stderr,
			"tennis: warning — binding to %s exposes an unauthenticated API on your network\n", *addr)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           newServeMux(db),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Ctrl-C finishes in-flight requests rather than killing them mid-write.
	// SQLite would recover through the WAL either way; exiting cleanly means it
	// never has to.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Fprintf(os.Stderr, "tennis: serving %s on http://%s\n", db.Path(), *addr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "tennis: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// newServeMux builds the API. Separate from cmdServe so tests can drive it
// through httptest without a real listener or signal handling.
func newServeMux(db *tennis.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/namespaces", func(w http.ResponseWriter, r *http.Request) {
		infos, err := db.ListNamespaces(r.Context())
		respond(w, infos, err)
	})
	mux.HandleFunc("POST /v1/namespaces/{ns}/write", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Documents []tennis.Document `json:"documents"`
			Model     string            `json:"model,omitempty"`
			OpenAI    string            `json:"openai_model,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondStatus(w, http.StatusBadRequest, err)
			return
		}
		name := r.PathValue("ns")
		ns, err := db.Namespace(r.Context(), name)
		if errors.Is(err, tennis.ErrNamespaceNotFound) {
			ns, err = db.CreateNamespace(r.Context(), name, tennis.NamespaceOptions{
				Model: body.Model, OpenAIModel: body.OpenAI,
			})
		}
		// Only "not found" triggers a create; any other open failure (missing
		// key, model mismatch) must be reported as itself.
		if err != nil {
			respond(w, nil, err)
			return
		}
		res, err := ns.Write(r.Context(), body.Documents)
		respond(w, res, err)
	})
	mux.HandleFunc("POST /v1/namespaces/{ns}/query", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text  string `json:"text"`
			TopK  int    `json:"top_k"`
			Mode  string `json:"mode"`
			Where string `json:"where"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondStatus(w, http.StatusBadRequest, err)
			return
		}
		ns, err := db.Namespace(r.Context(), r.PathValue("ns"))
		if err != nil {
			respond(w, nil, err)
			return
		}
		filter, err := parseWhere(body.Where)
		if err != nil {
			respondStatus(w, http.StatusBadRequest, err)
			return
		}
		results, err := ns.Query(r.Context(), tennis.Query{
			Text: body.Text, TopK: body.TopK, Mode: tennis.Mode(body.Mode), Filter: filter,
		})
		respond(w, map[string]any{"results": results}, err)
	})
	mux.HandleFunc("POST /v1/namespaces/{ns}/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			respondStatus(w, http.StatusBadRequest, err)
			return
		}
		ns, err := db.Namespace(r.Context(), r.PathValue("ns"))
		if err != nil {
			respond(w, nil, err)
			return
		}
		n, err := ns.Delete(r.Context(), body.IDs)
		respond(w, map[string]int{"deleted": n}, err)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"status": "ok", "version": version, "db": db.Path()}, nil)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		mux.ServeHTTP(w, r)
	})
}

// respond writes a JSON body, mapping known failures to useful statuses. The
// sentinel is matched with errors.Is; the substring checks catch the remaining
// errors that have no sentinel yet. Everything unrecognized is a 500 rather
// than being guessed at.
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, tennis.ErrNamespaceNotFound), strings.Contains(err.Error(), "not found"):
			status = http.StatusNotFound
		case strings.Contains(err.Error(), "invalid"), strings.Contains(err.Error(), "is empty"),
			strings.Contains(err.Error(), "cannot parse"):
			status = http.StatusBadRequest
		}
		respondStatus(w, status, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func respondStatus(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func isLoopback(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
