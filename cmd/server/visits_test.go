package main

import (
	"context"
	"fmt"
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

// The clock is a parameter, so every window here is exercised in nanoseconds and nothing
// sleeps. A test that waits 30 real minutes is a test nobody runs.
var t0 = time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

func newTestVisits() *visits {
	return newVisits(notify.Nop{}, slog.New(slog.DiscardHandler), t0)
}

// statePath is one real poll route. The cluster segment is deliberately spelled out rather
// than built from a constant: these tests exist to catch the route shape drifting away from
// what isStatePoll matches, and a shared constant would move both sides together and catch
// nothing.
const statePath = "/api/replication/state"

// poll is one dashboard poll from one visitor.
func poll(ip, ua string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, statePath, nil)
	r.Header.Set("X-Forwarded-For", ip)
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Origin", "https://dashboard.test")
	return r
}

const chrome = "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// The whole reason this type exists: the dashboard polls once a second, so a push per
// request would be a push per second, per tab, forever.
func TestOneVisitorPollingIsOneVisit(t *testing.T) {
	v := newTestVisits()

	if _, ok := v.visit(poll("1.2.3.4", chrome), t0); !ok {
		t.Fatal("the first poll did not count as a visit")
	}
	// A minute of polling, once a second.
	for i := 1; i <= 60; i++ {
		if _, ok := v.visit(poll("1.2.3.4", chrome), t0.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("poll %d pushed again; one visitor must be one push", i)
		}
	}
}

// The window is an IDLE timeout, not a fixed one: it refreshes on every poll. A tab left
// open all afternoon is one visit — under a fixed window it would push every 30 minutes at
// somebody who never left.
func TestATabLeftOpenStaysOneVisit(t *testing.T) {
	v := newTestVisits()
	v.visit(poll("1.2.3.4", chrome), t0)

	// Four hours of steady polling, well past visitWindow, never idle for long.
	for at := time.Minute; at < 4*time.Hour; at += time.Minute {
		if _, ok := v.visit(poll("1.2.3.4", chrome), t0.Add(at)); ok {
			t.Fatalf("pushed again at +%s while the tab was still polling", at)
		}
	}
}

func TestAVisitorWhoLeavesAndReturnsIsANewVisit(t *testing.T) {
	v := newTestVisits()
	v.visit(poll("1.2.3.4", chrome), t0)

	back := t0.Add(visitWindow + time.Minute)
	if _, ok := v.visit(poll("1.2.3.4", chrome), back); !ok {
		t.Fatal("a visitor returning after the idle window did not count as a new visit")
	}
}

// The notification has to say WHO, not just that somebody came.
func TestTheNotificationCarriesTheVisitorsIP(t *testing.T) {
	v := newTestVisits()

	n, ok := v.visit(poll("203.0.113.9", chrome), t0)
	if !ok {
		t.Fatal("the first poll did not count as a visit")
	}
	if !strings.Contains(n.Body, "203.0.113.9") {
		t.Errorf("body = %q, want the visitor's IP in it", n.Body)
	}
	if !strings.Contains(n.Body, "Chrome on Windows") {
		t.Errorf("body = %q, want the client described too", n.Body)
	}
}

func TestDifferentVisitorsEachNotify(t *testing.T) {
	v := newTestVisits()

	if _, ok := v.visit(poll("1.2.3.4", chrome), t0); !ok {
		t.Fatal("visitor A did not notify")
	}
	if _, ok := v.visit(poll("5.6.7.8", chrome), t0); !ok {
		t.Fatal("visitor B did not notify; the two must not dedup onto each other")
	}
}

// The API is public. A bot sweeping it must not become a denial-of-service on a phone.
func TestTheHourlyCapHoldsUnderAFlood(t *testing.T) {
	v := newTestVisits()

	var pushes int
	for i := range 500 {
		// Every request a brand new visitor, so dedup never fires and only the cap can.
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		if _, ok := v.visit(poll(ip, chrome), t0.Add(time.Duration(i)*time.Second)); ok {
			pushes++
		}
	}
	if pushes > visitsPerHour {
		t.Fatalf("sent %d pushes in an hour, cap is %d", pushes, visitsPerHour)
	}

	// ...and the budget comes back for the next hour, rather than latching off forever.
	later := t0.Add(time.Hour + time.Minute)
	if _, ok := v.visit(poll("99.99.99.99", chrome), later); !ok {
		t.Error("the hourly budget never reset")
	}
}

// The health check (GET /) and every mutating route must be invisible: only the dashboard's
// own poll is a visit.
func TestOnlyTheStatePollCounts(t *testing.T) {
	v := newTestVisits()
	sent := make(chan notify.Notification, 8)
	v.to = notifierChan(sent)

	srv := v.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	}))

	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/", nil),
		httptest.NewRequest(http.MethodPost, "/api/replication/kill", nil),
		httptest.NewRequest(http.MethodPost, statePath, nil),   // right path, wrong method
		httptest.NewRequest(http.MethodGet, "/api/state", nil), // the pre-cluster path: no longer a route
	} {
		srv.ServeHTTP(httptest.NewRecorder(), r)
	}
	select {
	case n := <-sent:
		t.Fatalf("a non-dashboard request notified: %q", n.Body)
	case <-time.After(100 * time.Millisecond):
	}

	srv.ServeHTTP(httptest.NewRecorder(), poll("1.2.3.4", chrome))
	select {
	case n := <-sent:
		if n.Title == "" || n.Body == "" {
			t.Errorf("empty notification: %+v", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the dashboard poll did not notify")
	}
}

func TestClientIPPrefersTheForwardedForClient(t *testing.T) {
	// Behind a proxy, RemoteAddr is the proxy: without XFF every visitor dedups onto one.
	r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", " 203.0.113.7 , 70.41.3.18 , 150.172.238.178 ")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want the first XFF entry", got)
	}

	// No proxy: fall back to RemoteAddr, minus the port.
	r = httptest.NewRequest(http.MethodGet, "/api/state", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want the host without the port", got)
	}
}

// Edge and Opera both claim "Chrome", and Chrome claims "Safari". Test order first, or
// every visitor is a Chrome user.
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
	if got := source(poll("1.2.3.4", chrome)); got != "from https://dashboard.test" {
		t.Errorf("source = %q, want the Origin", got)
	}
	if got := source(httptest.NewRequest(http.MethodGet, "/api/state", nil)); got != "direct API hit" {
		t.Errorf("source = %q, want the no-Origin case named", got)
	}
}

// The production wiring, end to end: a real cluster behind the real routes(), with the real
// ntfy transport pointed at a local server. Everything above is a unit; this is the only
// thing that proves routes() actually installs the middleware, in an order where withCORS
// and withLogging do not swallow it.
func TestARealStatePollPushesThroughRoutes(t *testing.T) {
	got := make(chan string, 4)
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.URL.Path + " " + string(b)
	}))
	defer ntfy.Close()

	// routes() reads these through notify.FromEnv. t.Setenv restores them on the way out.
	t.Setenv("NTFY_TOPIC", "smoke-topic")
	t.Setenv("NTFY_SERVER", ntfy.URL)

	c := cluster.New(3, 1, 2*time.Second, "n0", "n1", "n2")
	if err := c.Start(); err != nil {
		t.Fatalf("start cluster: %v", err)
	}
	defer c.Close()

	h := routes(map[string]*cluster.Cluster{"replication": c}, slog.New(slog.DiscardHandler))

	// The browser's CORS preflight must not count as a visit; the poll that follows must.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodOptions, statePath, nil))
	h.ServeHTTP(httptest.NewRecorder(), poll("203.0.113.9", chrome))

	select {
	case msg := <-got:
		if !strings.HasPrefix(msg, "/smoke-topic ") {
			t.Errorf("published to %q, want the topic as the path", msg)
		}
		for _, want := range []string{"203.0.113.9", "Chrome on Windows", "https://dashboard.test"} {
			if !strings.Contains(msg, want) {
				t.Errorf("notification lost %q: %q", want, msg)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a dashboard poll through routes() did not push a notification")
	}

	// Exactly one: the preflight did not also count.
	select {
	case msg := <-got:
		t.Fatalf("a second notification arrived (the OPTIONS preflight counted?): %q", msg)
	case <-time.After(300 * time.Millisecond):
	}
}

// notifierChan hands every notification to a channel, so a test can wait for the goroutine
// the middleware spawns instead of sleeping and hoping.
type notifierChan chan notify.Notification

func (c notifierChan) Notify(_ context.Context, n notify.Notification) error {
	c <- n
	return nil
}
