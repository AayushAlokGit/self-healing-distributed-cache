package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
	"github.com/AayushAlokGit/self-healing-distributed-cache/notify"
)

// maxSeed bounds one POST /api/{cluster}/seed batch. The dashboard stops the user lower
// (MAX_SEED in frontend/src/components/WritePanel.tsx), but this is the copy that
// enforces anything at all, since a client can curl the API directly.
const maxSeed = 5000

// clusterHandler is a handler that has already been told which cluster it is for. handle
// (below) supplies the cluster; everything else is an ordinary http.HandlerFunc.
type clusterHandler func(*cluster.Cluster, http.ResponseWriter, *http.Request)

// routes wires the control API over every demo cluster (see demoClusters in main.go). The
// UI is the React app in frontend/, served separately (Vite in dev, a static host in prod)
// and talking to this API.
//
// Every route is scoped to exactly one cluster by its {cluster} segment. There is
// deliberately no route that reaches more than one, so no dashboard bug can make an action
// on one demo disturb another.
func routes(clusters map[string]*cluster.Cluster, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// handle registers a cluster-scoped route, resolving {cluster} so no handler below has
	// to think about it. An unknown name 404s rather than falling back to a default:
	// quietly serving "some cluster" to a caller who named one is how you debug the wrong
	// ring for an hour. GET / lists the valid names.
	handle := func(pattern string, fn clusterHandler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("cluster")
			c, ok := clusters[id]
			if !ok {
				writeErr(w, http.StatusNotFound, "unknown cluster "+id)
				return
			}
			fn(c, w, r)
		})
	}

	handle("GET /api/{cluster}/state", func(c *cluster.Cluster, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, c.State())
	})

	handle("POST /api/{cluster}/set", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		// Milliseconds, matching the ttlMs KeyState reports back. <= 0 or absent means
		// the key never expires. Via names which node coordinates the write ("" = any live
		// node); a via that is not live comes back as a 400 via coordStatus.
		var body struct {
			Key, Value string
			TTLMs      int64  `json:"ttlMs"`
			Via        string `json:"via"`
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
		if err := c.Set(body.Key, body.Value, ttl, body.Via); err != nil {
			writeErr(w, coordStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	handle("GET /api/{cluster}/get", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			writeErr(w, http.StatusBadRequest, "key is required")
			return
		}
		// via names which node coordinates the read ("" = any live node); a via naming a node
		// that is not live is a 400, so reads on either side of a partition are deterministic.
		res, err := c.Get(key, r.URL.Query().Get("via"))
		if err != nil {
			writeErr(w, coordStatus(err), err.Error())
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
			"conflict":    res.Conflict,
			"siblings":    res.Siblings,
		})
	})

	handle("POST /api/{cluster}/seed", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
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

	// POST, not DELETE, to match every other mutation here — and because withCORS below
	// allows GET/POST/OPTIONS only, so a DELETE would fail the browser's preflight. The
	// node-to-node protocol does use real DELETE verbs; this is just the control API.
	handle("POST /api/{cluster}/delete", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		var body struct{ Key string }
		if !readJSON(w, r, &body) {
			return
		}
		if body.Key == "" {
			writeErr(w, http.StatusBadRequest, "key is required")
			return
		}
		dropped, err := c.Delete(body.Key)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		// dropped may be empty: no node held the key. That is a successful delete, not a
		// 404 — the caller asked for the key to be gone, and it is.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dropped": dropped})
	})

	handle("POST /api/{cluster}/clear", func(c *cluster.Cluster, w http.ResponseWriter, _ *http.Request) {
		keys, err := c.Clear()
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keys": keys})
	})

	handle("POST /api/{cluster}/kill", nodeAction((*cluster.Cluster).Kill))
	handle("POST /api/{cluster}/revive", nodeAction((*cluster.Cluster).Revive))

	handle("POST /api/{cluster}/pause", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
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

	// Cut splits the cluster in two so neither side can reach the other (data and health),
	// the fault the CAP demo is built on. A via that is not a live node — here, any id on
	// either side — is the caller's mistake, so coordStatus maps *NoSuchNodeError to a 400.
	handle("POST /api/{cluster}/cut", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		var body struct {
			SideA []string `json:"sideA"`
			SideB []string `json:"sideB"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if len(body.SideA) == 0 || len(body.SideB) == 0 {
			writeErr(w, http.StatusBadRequest, "cut needs two non-empty sides")
			return
		}
		if err := c.Cut(body.SideA, body.SideB); err != nil {
			writeErr(w, coordStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	handle("POST /api/{cluster}/mend", func(c *cluster.Cluster, w http.ResponseWriter, _ *http.Request) {
		c.Mend()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// A hint at the root, since the UI lives elsewhere. It names the clusters, so anyone
	// curling this can find the routes without reading the source.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":  "self-healing-distributed-cache control API",
			"clusters": slices.Sorted(maps.Keys(clusters)),
			"ui":       "run the React app in frontend/ (npm run dev), or see /api/{cluster}/state",
		})
	})

	var h http.Handler = mux

	// Off unless the environment configures a notifier, so local development never pushes.
	// The middleware only exists when it is on — no disabled path to pay for.
	if to, on := notify.FromEnv(); on {
		log.Info("visit notifications on", "via", to)
		h = newVisits(to, log, time.Now()).middleware(h)
	} else {
		log.Info("visit notifications off ($NTFY_TOPIC unset)")
	}

	// CORS: the React app is a different origin in both dev and prod.
	return withLogging(withCORS(h), log)
}

// IsStatePoll reports whether r is a dashboard state poll: GET /api/{cluster}/state.
//
// ⚠️ Match the SHAPE, never a literal path. Two callers need this — the log level below and
// the visit notifier (visits.go) — and both used to compare r.URL.Path against the constant
// "/api/state". The moment the route gained a {cluster} segment, both comparisons silently
// stopped matching anything: every poll would have logged at Info (burying each kill under
// thousands of them) and visit notifications would have stopped firing outright. Nothing
// would have errored, and no test would have failed.
//
// This middleware cannot use r.PathValue("cluster") instead: it wraps the mux, and the mux
// sets path values on a *clone* of the request during routing, which this r never sees.
// ⚠️ Exactly /api/{one non-empty segment}/state. A looser prefix+suffix test also matches
// the pre-cluster "/api/state", which is now a 404 — and counting a visit for a request the
// mux refuses is a push about nothing.
func isStatePoll(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/")
	if !ok {
		return false
	}
	id, tail, ok := strings.Cut(rest, "/")
	return ok && id != "" && tail == "state"
}

// withLogging records every HTTP request: what it asked for, what it got, how long it
// took. Outermost layer, so the duration covers CORS, the handler, and the cluster's
// round trips to the nodes.
func withLogging(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		h.ServeHTTP(rec, r)

		// State polls arrive several times a second, per cluster, so they log at Debug: at
		// Info they would bury every kill and heal under thousands of identical polls.
		level := slog.LevelInfo
		switch {
		case isStatePoll(r):
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

// nodeAction adapts Kill/Revive into a handler reading {id}. It takes a **method
// expression** — `(*cluster.Cluster).Kill`, not `c.Kill` — which yields a plain
// func(*cluster.Cluster, string) error with the receiver as its first argument. That is
// what lets one handler serve every cluster: the receiver arrives per request from handle,
// so there is no cluster to bind at wiring time.
func nodeAction(fn func(*cluster.Cluster, string) error) clusterHandler {
	return func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		if !readJSON(w, r, &body) {
			return
		}
		if err := fn(c, body.ID); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// coordStatus maps a coordinator-resolution failure to an HTTP status. Naming a via that is
// not a live node is the caller's mistake — a 400 — and it must fail loudly, since a
// deterministic coordinator is the whole reason via exists. Every other failure (no live node
// at all, the coordinator unreachable mid-request) is the cluster's, not the request's, so it
// stays a 502, matching the no-via behavior these handlers had before.
func coordStatus(err error) int {
	var nse *cluster.NoSuchNodeError
	if errors.As(err, &nse) {
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
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
