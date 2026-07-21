package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
	"github.com/AayushAlokGit/self-healing-distributed-cache/notify"
)

// twoClusters is the production shape: the dashboard's two demo tabs behind one mux.
func twoClusters(t *testing.T) http.Handler {
	t.Helper()
	clusters := map[string]*cluster.Cluster{}
	for _, id := range []string{"replication", "cap"} {
		c := cluster.New(3, 1, 300*time.Millisecond, "n0", "n1", "n2", "n3", "n4")
		if err := c.Start(); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		t.Cleanup(c.Close)
		clusters[id] = c
	}
	// Nop: these tests are about the API, and faults_test.go covers the pushing.
	return routes(clusters, notify.Nop{}, slog.New(slog.DiscardHandler))
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// state decodes just enough of GET /api/{id}/state to assert on it.
func state(t *testing.T, h http.Handler, id string) struct {
	AliveCount int `json:"aliveCount"`
	Keys       []struct {
		Key string `json:"key"`
	} `json:"keys"`
} {
	t.Helper()
	var s struct {
		AliveCount int `json:"aliveCount"`
		Keys       []struct {
			Key string `json:"key"`
		} `json:"keys"`
	}
	w := do(t, h, http.MethodGet, "/api/"+id+"/state", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET state for %s: %d — %s", id, w.Code, w.Body)
	}
	if err := json.NewDecoder(w.Body).Decode(&s); err != nil {
		t.Fatalf("decode state for %s: %v", id, err)
	}
	return s
}

// The whole reason the {cluster} segment exists. cluster/ already guarantees two Cluster
// values cannot touch each other; this proves the HTTP layer picks the RIGHT one, which is
// the only way the isolation can still be lost.
func TestAClusterActionCannotReachTheOtherCluster(t *testing.T) {
	h := twoClusters(t)

	// A write to one tab must not appear on the other.
	if w := do(t, h, http.MethodPost, "/api/cap/set", `{"key":"only-in-cap","value":"x"}`); w.Code != http.StatusOK {
		t.Fatalf("set on cap: %d — %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"cap", true}, {"replication", false}} {
		w := do(t, h, http.MethodGet, "/api/"+tc.id+"/get?key=only-in-cap", "")
		var got struct{ Found bool }
		json.NewDecoder(w.Body).Decode(&got)
		if got.Found != tc.want {
			t.Errorf("get only-in-cap on %s: found=%v, want %v", tc.id, got.Found, tc.want)
		}
	}

	// A kill on one tab must not be felt on the other. This is the failure a stranger would
	// actually hit: killing a node on the replication demo while a partition run is live on
	// the CAP tab.
	if w := do(t, h, http.MethodPost, "/api/replication/kill", `{"id":"n2"}`); w.Code != http.StatusOK {
		t.Fatalf("kill on replication: %d — %s", w.Code, w.Body)
	}
	if got := state(t, h, "replication").AliveCount; got != 4 {
		t.Errorf("replication aliveCount = %d, want 4 (its own node died)", got)
	}
	if got := state(t, h, "cap").AliveCount; got != 5 {
		t.Errorf("CROSS-TALK: cap aliveCount = %d, want 5 — a kill on replication was felt here", got)
	}
}

// The HTTP surface of the coordinator picker. set takes a "via" body field, get takes a
// "via" query param, and both must route through exactly that node — the response's
// coordinator proves it. A via naming a node that is not live is a 400, not a silent reroute:
// the partition demo depends on being able to name which side coordinates.
func TestViaPicksTheCoordinatorOverHTTP(t *testing.T) {
	h := twoClusters(t)

	if w := do(t, h, http.MethodPost, "/api/cap/set", `{"key":"k","value":"v","via":"n3"}`); w.Code != http.StatusOK {
		t.Fatalf("set via n3: %d — %s", w.Code, w.Body)
	}
	w := do(t, h, http.MethodGet, "/api/cap/get?key=k&via=n3", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get via n3: %d — %s", w.Code, w.Body)
	}
	var got struct {
		Found       bool
		Coordinator string
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode get via n3: %v", err)
	}
	if !got.Found || got.Coordinator != "n3" {
		t.Errorf("get via n3: found=%v coordinator=%q, want true/n3 — via must pin the coordinator", got.Found, got.Coordinator)
	}

	// Kill the named node: it is no longer a valid via, so set and get that name it are 400s,
	// never a silent reroute to a live node.
	if w := do(t, h, http.MethodPost, "/api/cap/kill", `{"id":"n3"}`); w.Code != http.StatusOK {
		t.Fatalf("kill n3: %d — %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/cap/set", `{"key":"k","value":"v","via":"n3"}`}, // just killed
		{http.MethodGet, "/api/cap/get?key=k&via=n3", ""},                       // just killed
		{http.MethodGet, "/api/cap/get?key=k&via=n9", ""},                       // never existed
	} {
		if w := do(t, h, tc.method, tc.path, tc.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s via a dead/unknown node = %d, want 400 — %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// An unknown name must 404, not fall through to a default. Serving "some cluster" to a
// caller who named one is a silent wrong answer.
func TestAnUnknownClusterIs404(t *testing.T) {
	h := twoClusters(t)
	for _, path := range []string{"/api/nope/state", "/api/nope/kill", "/api/NOPE/state"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/kill") {
			method = http.MethodPost
		}
		if w := do(t, h, method, path, `{"id":"n0"}`); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", method, path, w.Code)
		}
	}
	// The pre-cluster routes are gone, not silently aliased to a default cluster.
	if w := do(t, h, http.MethodGet, "/api/state", ""); w.Code == http.StatusOK {
		t.Error("GET /api/state still answers 200; it must not resolve to a default cluster")
	}
}

// isStatePoll drives both the log level and the visit notifier, and neither has anything to
// fail loudly if it silently stops matching (server.go). So it is tested directly.
func TestIsStatePollMatchesTheRouteShapeOnly(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/replication/state", true},
		{http.MethodGet, "/api/cap/state", true},
		{http.MethodPost, "/api/cap/state", false},       // right path, wrong method
		{http.MethodGet, "/api/state", false},            // the pre-cluster path: now a 404
		{http.MethodGet, "/api//state", false},           // empty cluster name
		{http.MethodGet, "/api/cap/statefulness", false}, // suffix must be the whole segment
		{http.MethodGet, "/api/cap/keys/state", false},   // one segment only
		{http.MethodGet, "/api/cap/kill", false},
		{http.MethodGet, "/", false},
	} {
		got := isStatePoll(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want {
			t.Errorf("isStatePoll(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
