package main

import (
	"encoding/json"
	"net/http"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
)

// routes wires the control API. The UI is the React app in frontend/, served
// separately (Vite in dev, a static host in prod) and talking to this API.
func routes(c *cluster.Cluster) http.Handler {
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
	return withCORS(mux)
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
