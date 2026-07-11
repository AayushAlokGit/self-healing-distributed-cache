package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
)

// maxSeed bounds one POST /api/seed batch. The dashboard stops the user lower
// (MAX_SEED in frontend/src/components/WritePanel.tsx), but this is the copy that
// enforces anything at all, since a client can curl the API directly.
const maxSeed = 5000

// routes wires the control API. The UI is the React app in frontend/, served
// separately (Vite in dev, a static host in prod) and talking to this API.
func routes(c *cluster.Cluster, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, c.State())
	})

	mux.HandleFunc("POST /api/set", func(w http.ResponseWriter, r *http.Request) {
		// Milliseconds, matching the ttlMs KeyState reports back. <= 0 or absent means
		// the key never expires.
		var body struct {
			Key, Value string
			TTLMs      int64 `json:"ttlMs"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if body.Key == "" {
			writeErr(w, http.StatusBadRequest, "key is required")
			return
		}
		var ttl time.Duration
		if body.TTLMs > 0 {
			ttl = time.Duration(body.TTLMs) * time.Millisecond
		}
		if err := c.Set(body.Key, body.Value, ttl); err != nil {
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
		res, err := c.Get(key)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"found":       res.Found,
			"value":       res.Value,
			"coordinator": res.Coordinator,
			"servedBy":    res.ServedBy,
			"primary":     res.Primary(),
			"fallback":    res.Fallback(),
			"path":        res.Path,
		})
	})

	mux.HandleFunc("POST /api/seed", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ N int }
		if !readJSON(w, r, &body) {
			return
		}
		if body.N <= 0 {
			body.N = 12
		}
		// Seed writes the whole batch synchronously before replying, so an unbounded n is a
		// request that never returns. Refuse rather than clamp: seeding fewer keys than the
		// caller asked for, without saying so, reads as a bug from the other side.
		if body.N > maxSeed {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("seed n=%d exceeds the limit of %d keys per batch", body.N, maxSeed))
			return
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

	// A hint at the root, since the UI lives elsewhere.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "self-healing-distributed-cache control API",
			"ui":      "run the React app in frontend/ (npm run dev), or see /api/state",
		})
	})

	// CORS: the React app is a different origin in both dev and prod.
	return withLogging(withCORS(mux), log)
}

// noisyPaths are polled several times a second, so they log at Debug: at Info they
// would bury every kill and heal under thousands of identical polls.
var noisyPaths = map[string]bool{"/api/state": true}

// withLogging records every HTTP request: what it asked for, what it got, how long it
// took. Outermost layer, so the duration covers CORS, the handler, and the cluster's
// round trips to the nodes.
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

// statusRecorder wraps a ResponseWriter to remember the status code and byte count the
// handler wrote; a ResponseWriter will not report either back.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

// WriteHeader records the status on the way through. A handler may never call it (a
// bare Write implies 200), which is why status is seeded to 200 at construction.
func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// withCORS allows any origin to call the control API, and answers preflight OPTIONS
// directly.
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
