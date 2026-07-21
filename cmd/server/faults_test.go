package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
	"github.com/AayushAlokGit/self-healing-distributed-cache/notify"
)

// The clock is a parameter, so the hourly window is exercised in nanoseconds and nothing
// sleeps. A test that waits an hour is a test nobody runs.
var t0 = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

const chrome = "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// The API is public and a kill is one unauthenticated POST. A script holding the button down
// must not become a denial-of-service on a phone.
func TestTheHourlyCapHoldsUnderAFlood(t *testing.T) {
	f := newFaults(notify.Nop{}, slog.New(slog.DiscardHandler), t0)

	var sent int
	for i := range 500 {
		if f.allow(t0.Add(time.Duration(i) * time.Second)) {
			sent++
		}
	}
	if sent != faultsPerHour {
		t.Fatalf("sent %d pushes in an hour, cap is %d", sent, faultsPerHour)
	}

	// ...and the budget comes back for the next hour, rather than latching off forever.
	if !f.allow(t0.Add(time.Hour + time.Minute)) {
		t.Error("the hourly budget never reset")
	}
}

// clickFrom is one dashboard button press: a browser fetch, from a real visitor, cross-origin.
func clickFrom(ip, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Forwarded-For", ip)
	r.Header.Set("User-Agent", chrome)
	r.Header.Set("Origin", "https://dashboard.test")
	return r
}

// testRoutes is the real routes() over a real cluster, with the notifier swapped for a
// channel. Everything below drives HTTP, not faults directly: the point of these tests is
// that the right ROUTES push, which a unit test of faults cannot tell you.
func testRoutes(t *testing.T) (http.Handler, chan notify.Notification) {
	t.Helper()

	c := cluster.New(3, 1, 2*time.Second, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	t.Cleanup(c.Close)

	// Buffered so faults.send() never blocks on a test that stops reading.
	sent := make(chan notify.Notification, 16)
	h := routes(map[string]*cluster.Cluster{"replication": c}, notifierChan(sent), slog.New(slog.DiscardHandler))
	return h, sent
}

// waitForPush waits for the goroutine announce() spawns, rather than sleeping and hoping.
func waitForPush(t *testing.T, sent <-chan notify.Notification) notify.Notification {
	t.Helper()
	select {
	case n := <-sent:
		return n
	case <-time.After(2 * time.Second):
		t.Fatal("no notification arrived")
		return notify.Notification{}
	}
}

func wantNoPush(t *testing.T, sent <-chan notify.Notification, why string) {
	t.Helper()
	select {
	case n := <-sent:
		t.Fatalf("%s: %q", why, n.Body)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestKillingANodePushes(t *testing.T) {
	h, sent := testRoutes(t)

	h.ServeHTTP(httptest.NewRecorder(),
		clickFrom("203.0.113.9", http.MethodPost, "/api/replication/kill", `{"id":"n1"}`))

	n := waitForPush(t, sent)
	// The notification has to say WHICH node, in WHICH cluster, and who did it — "a fault
	// happened somewhere" is not worth a buzz.
	for _, want := range []string{"n1", "replication", "killed", "203.0.113.9", "Chrome on Windows"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body = %q, want %q in it", n.Body, want)
		}
	}
	if n.Title == "" {
		t.Error("no title")
	}
}

func TestCuttingTheNetworkPushesBothSides(t *testing.T) {
	h, sent := testRoutes(t)

	h.ServeHTTP(httptest.NewRecorder(),
		clickFrom("203.0.113.9", http.MethodPost, "/api/replication/cut",
			`{"sideA":["n0","n1"],"sideB":["n2","n3","n4"]}`))

	n := waitForPush(t, sent)
	for _, want := range []string{"cut", "n0", "n1", "n2", "n3", "n4"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body = %q, want %q in it", n.Body, want)
		}
	}
}

// The whole reason this is not a middleware. A middleware sees POST /api/{cluster}/kill and
// pushes; only the handler knows the cluster refused, and "n7 killed" about a node that does
// not exist is a lie the reader cannot check.
func TestAKillThatFailsDoesNotPush(t *testing.T) {
	h, sent := testRoutes(t)

	for _, tc := range []struct{ path, body string }{
		{"/api/replication/kill", `{"id":"n7"}`},                // no such node
		{"/api/replication/kill", `{`},                          // bad JSON
		{"/api/nope/kill", `{"id":"n1"}`},                       // no such cluster
		{"/api/replication/cut", `{"sideA":[],"sideB":["n1"]}`}, // a cut needs two sides
		{"/api/replication/cut", `{"sideA":["n0"],"sideB":["n9"]}`},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, clickFrom("203.0.113.9", http.MethodPost, tc.path, tc.body))
		if w.Code < 400 {
			t.Fatalf("POST %s %s = %d, want a refusal (the test proves nothing otherwise)", tc.path, tc.body, w.Code)
		}
	}
	wantNoPush(t, sent, "a refused fault pushed anyway")
}

// Only the two fault buttons. Everything else — the poll storm the dashboard makes, the
// health check, and the actions that are somebody warming up — stays silent.
func TestOnlyKillAndCutPush(t *testing.T) {
	h, sent := testRoutes(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/", ""},
		{http.MethodGet, "/api/replication/state", ""},
		{http.MethodGet, "/api/replication/get?key=k", ""},
		{http.MethodPost, "/api/replication/set", `{"key":"k","value":"v"}`},
		{http.MethodPost, "/api/replication/seed", `{"n":3}`},
		{http.MethodPost, "/api/replication/delete", `{"key":"k"}`},
		{http.MethodPost, "/api/replication/clear", `{}`},
		{http.MethodPost, "/api/replication/pause", `{"id":"n1","paused":true}`},
		{http.MethodPost, "/api/replication/quorum", `{"w":2,"rRead":2}`},
		{http.MethodPost, "/api/replication/mend", `{}`},
		{http.MethodOptions, "/api/replication/kill", ""}, // the CORS preflight is not a click
	} {
		h.ServeHTTP(httptest.NewRecorder(), clickFrom("1.2.3.4", tc.method, tc.path, tc.body))
	}
	wantNoPush(t, sent, "a non-fault request pushed")

	// Revive is the fix, not the fault: it must not push either — but only after a kill that
	// did, so a revive with nothing to revive is not what kept this quiet.
	h.ServeHTTP(httptest.NewRecorder(),
		clickFrom("1.2.3.4", http.MethodPost, "/api/replication/kill", `{"id":"n1"}`))
	waitForPush(t, sent)
	h.ServeHTTP(httptest.NewRecorder(),
		clickFrom("1.2.3.4", http.MethodPost, "/api/replication/revive", `{"id":"n1"}`))
	wantNoPush(t, sent, "revive pushed")
}

// The production wiring end to end: the real routes(), the real ntfy transport, pointed at a
// local server. Everything above swaps the Notifier out; this is the only thing that proves
// notify.FromEnv() is actually read and the message survives the HTTP encoding.
func TestARealKillPushesThroughNtfy(t *testing.T) {
	got := make(chan string, 4)
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.URL.Path + " " + r.Header.Get("Title") + " " + string(b)
	}))
	defer ntfy.Close()

	// Read below by notify.FromEnv. t.Setenv restores them on the way out.
	t.Setenv("NTFY_TOPIC", "smoke-topic")
	t.Setenv("NTFY_SERVER", ntfy.URL)

	c := cluster.New(3, 1, 2*time.Second, "n0", "n1", "n2")
	if err := c.Start(); err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	defer c.Close()

	// FromEnv, not a hand-built Ntfy: reading the env vars is part of what this proves.
	to, on := notify.FromEnv()
	if !on {
		t.Fatal("FromEnv did not turn notifications on with $NTFY_TOPIC set")
	}

	h := routes(map[string]*cluster.Cluster{"replication": c}, to, slog.New(slog.DiscardHandler))
	h.ServeHTTP(httptest.NewRecorder(),
		clickFrom("203.0.113.9", http.MethodPost, "/api/replication/kill", `{"id":"n1"}`))

	select {
	case msg := <-got:
		if !strings.HasPrefix(msg, "/smoke-topic ") {
			t.Errorf("published to %q, want the topic as the path", msg)
		}
		for _, want := range []string{"n1", "203.0.113.9", "Chrome on Windows", "https://dashboard.test"} {
			if !strings.Contains(msg, want) {
				t.Errorf("notification lost %q: %q", want, msg)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a kill through routes() did not push a notification")
	}
}

func TestClientIPPrefersTheForwardedForClient(t *testing.T) {
	// Behind a proxy, RemoteAddr is the proxy: without XFF every visitor looks like one person.
	r := httptest.NewRequest(http.MethodGet, "/api/replication/state", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", " 203.0.113.7 , 70.41.3.18 , 150.172.238.178 ")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want the first XFF entry", got)
	}

	// No proxy: fall back to RemoteAddr, minus the port.
	r = httptest.NewRequest(http.MethodGet, "/api/replication/state", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want the host without the port", got)
	}
}

// Edge and Opera both claim "Chrome", and Chrome claims "Safari". Test order first, or
// everyone is a Chrome user.
func TestDescribeUAPicksTheMostSpecificMatch(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{chrome, "Chrome on Windows"},
		{"Mozilla/5.0 (Windows NT 10.0) Chrome/120.0 Safari/537.36 Edg/120.0", "Edge on Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Version/17.0 Safari/605.1.15", "Safari on macOS"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Version/17.0 Safari/604.1", "Safari on iPhone"},
		{"curl/8.4.0", "curl on unknown OS"},
		{"", "unknown client"},
	} {
		if got := describeUA(tc.ua); got != tc.want {
			t.Errorf("describeUA(%.40q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

func TestSourceSeparatesTheDashboardFromACurl(t *testing.T) {
	r := clickFrom("1.2.3.4", http.MethodPost, "/api/replication/kill", "")
	if got := source(r); got != "from https://dashboard.test" {
		t.Errorf("source = %q, want the Origin", got)
	}
	if got := source(httptest.NewRequest(http.MethodGet, "/", nil)); got != "direct API hit" {
		t.Errorf("source = %q, want the no-Origin case named", got)
	}
}

// notifierChan hands every notification to a channel, so a test can wait for the goroutine
// announce() spawns instead of sleeping and hoping.
type notifierChan chan notify.Notification

func (c notifierChan) Notify(_ context.Context, n notify.Notification) error {
	c <- n
	return nil
}
