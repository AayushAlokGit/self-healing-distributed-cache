package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
)

// routes wires the control API. The UI is the React app in frontend/, served
// separately (Vite in dev, a static host in prod) and talking to this API.
func routes(c *cluster.Cluster, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, c.State())
	})

	mux.HandleFunc("POST /api/set", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Key, Value string }
		if !readJSON(w, r, &body) {
			return
		}
		if body.Key == "" {
			writeErr(w, http.StatusBadRequest, "key is required")
			return
		}
		if err := c.Set(body.Key, body.Value); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			writeErr(w, http.StatusBadRequest, "key is required")
			return
		}
		v, found, err := c.Get(key)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"found": found, "value": v})
	})

	mux.HandleFunc("POST /api/seed", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ N int }
		if !readJSON(w, r, &body) {
			return
		}
		if body.N <= 0 {
			body.N = 12
		}
		if err := c.Seed(body.N); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/kill", nodeAction(c.Kill))
	mux.HandleFunc("POST /api/revive", nodeAction(c.Revive))

	mux.HandleFunc("POST /api/pause", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID     string
			Paused bool
		}
		if !readJSON(w, r, &body) {
			return
		}
		if err := c.Pause(body.ID, body.Paused); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// A hint at the root, since the UI lives elsewhere now.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "self-healing-distributed-cache control API",
			"ui":      "run the React app in frontend/ (npm run dev), or see /api/state",
		})
	})

	// CORS so the React app (a different origin in dev via Vite's proxy, or a
	// separate static host in prod) can call the API.
	return withLogging(withCORS(mux), log)
}

// noisyPaths are polled by the dashboard several times a second. Logging them at
// Info would bury every interesting line — a kill, a heal — under thousands of
// identical polls, which is the usual way an access log becomes useless. They log
// at Debug instead: still there when you go looking, never in the way.
var noisyPaths = map[string]bool{"/api/state": true}

// withLogging records every HTTP request the frontend makes: what it asked for,
// what it got, and how long it took. This is the outermost layer, so the duration
// includes everything inside it (CORS, handler, the cluster's network round trips
// to the nodes).
func withLogging(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		h.ServeHTTP(rec, r)

		level := slog.LevelInfo
		switch {
		case noisyPaths[r.URL.Path]:
			level = slog.LevelDebug
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		}

		log.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", rec.status,
			"bytes", rec.written,
			"took", time.Since(start).Round(time.Millisecond),
		)
	})
}

// statusRecorder wraps a ResponseWriter to remember the status code and byte count
// the handler wrote. An http.ResponseWriter will not tell you what it sent — the
// only way to log a response's status is to intercept the call that sets it.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

// WriteHeader records the status on the way through. Note the handler may never
// call it: a bare Write implies 200, which is why status is seeded to 200 above.
func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// withCORS allows any origin to call the control API — fine for a demo whose
// whole point is to be poked at. Answers preflight OPTIONS directly.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// nodeAction adapts a func(id) error (Kill/Revive) into a handler reading {id}.
func nodeAction(fn func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		if !readJSON(w, r, &body) {
			return
		}
		if err := fn(body.ID); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return false
	}
	return true
}
