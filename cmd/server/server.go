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
// to is where fault notifications go. It is a parameter rather than something routes() reads
// out of the environment itself, so a test can pass its own Notifier instead of reaching into
// a closure. notify.Nop{} turns them off — never nil, so no handler needs a "notifications
// enabled?" branch.
func routes(clusters map[string]*cluster.Cluster, to notify.Notifier, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Built before the routes, because the kill and cut handlers close over it.
	fx := newFaults(to, log, time.Now())

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

	// Kill notifies; revive passes nil. The interesting event is the fault, not the fix —
	// and a push per revive doubles the traffic to say nothing new.
	handle("POST /api/{cluster}/kill", nodeAction((*cluster.Cluster).Kill, fx.killed))
	handle("POST /api/{cluster}/revive", nodeAction((*cluster.Cluster).Revive, nil))

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
		// After the error check, so a cut naming a node that does not exist stays a silent 400.
		fx.cut(r, r.PathValue("cluster"), body.SideA, body.SideB)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	handle("POST /api/{cluster}/mend", func(c *cluster.Cluster, w http.ResponseWriter, _ *http.Request) {
		c.Mend()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// Quorum sets the consistency dial: W (write quorum) and R_read (read quorum). An out-of-range
	// pair is the caller's mistake, so SetQuorum's error maps to a 400. W+R_read>R holds the ring
	// and forbids stale reads (a partitioned side without a quorum refuses); otherwise it's eventual.
	handle("POST /api/{cluster}/quorum", func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		var body struct {
			W     int `json:"w"`
			RRead int `json:"rRead"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if err := c.SetQuorum(body.W, body.RRead); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "w": body.W, "rRead": body.RRead})
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

	// CORS: the React app is a different origin in both dev and prod.
	return withLogging(withCORS(mux), log)
}

// IsStatePoll reports whether r is a dashboard state poll: GET /api/{cluster}/state.
//
// ⚠️ Match the SHAPE, never a literal path. This used to compare r.URL.Path against the
// constant "/api/state", and the moment the route gained a {cluster} segment the comparison
// silently stopped matching anything: every poll would have logged at Info, burying each kill
// and heal under thousands of identical lines. Nothing would have errored, and no test would
// have failed — which is why routes_test.go tests this function directly.
//
// This runs in a middleware wrapping the mux, so it cannot use r.PathValue("cluster"): the
// mux sets path values on a *clone* of the request during routing, which this r never sees.
// ⚠️ Exactly /api/{one non-empty segment}/state — a looser prefix+suffix test also matches
// the pre-cluster "/api/state", which is now a 404.
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
//
// announce, if non-nil, is called only once fn has succeeded — an unknown node id is a 400
// and must not push. nil means this action is not worth a notification.
func nodeAction(fn func(*cluster.Cluster, string) error, announce func(*http.Request, string, string)) clusterHandler {
	return func(c *cluster.Cluster, w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		if !readJSON(w, r, &body) {
			return
		}
		if err := fn(c, body.ID); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if announce != nil {
			announce(r, r.PathValue("cluster"), body.ID)
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
