package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captured is what the fake ntfy server saw.
type captured struct {
	method, path, body string
	header             http.Header
}

// fakeNtfy stands in for ntfy.sh: a real HTTP server on a local port, so the transport is
// exercised end to end (headers, body, status handling) without leaving the machine.
func fakeNtfy(t *testing.T, status int) (*Ntfy, *captured) {
	t.Helper()
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = captured{method: r.Method, path: r.URL.Path, body: string(b), header: r.Header.Clone()}
		w.WriteHeader(status)
		io.WriteString(w, "rate limit reached")
	}))
	t.Cleanup(srv.Close)
	return NewNtfy(srv.URL, "secret-topic"), &got
}

func TestNtfyPublishesTheNotification(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)

	err := n.Notify(context.Background(), Notification{
		Title:    "Cache demo visit",
		Body:     "a1b2c3d4 · Chrome on Windows",
		Tags:     []string{"eyes", "computer"},
		Click:    "https://example.test/",
		Priority: High,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	// The topic is the path — that is the whole of ntfy's addressing.
	if got.path != "/secret-topic" {
		t.Errorf("path = %q, want /secret-topic", got.path)
	}
	// The message is the raw body, not a JSON field.
	if got.body != "a1b2c3d4 · Chrome on Windows" {
		t.Errorf("body = %q", got.body)
	}
	if h := got.header.Get("Title"); h != "Cache demo visit" {
		t.Errorf("Title = %q", h)
	}
	if h := got.header.Get("Tags"); h != "eyes,computer" {
		t.Errorf("Tags = %q, want comma-joined", h)
	}
	if h := got.header.Get("Click"); h != "https://example.test/" {
		t.Errorf("Click = %q", h)
	}
	if h := got.header.Get("Priority"); h != "4" {
		t.Errorf("Priority = %q, want 4 (High)", h)
	}
}

// A Notification that sets nothing but a body must still be a valid publish — the zero
// Priority has to mean "default" (3), not 0, which ntfy would reject.
func TestNtfyZeroValueNotificationIsValid(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)

	if err := n.Notify(context.Background(), Notification{Body: "hello"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if h := got.header.Get("Priority"); h != "3" {
		t.Errorf("Priority = %q, want 3 (default)", h)
	}
	for _, h := range []string{"Title", "Tags", "Click"} {
		if v := got.header.Get(h); v != "" {
			t.Errorf("%s = %q, want unset", h, v)
		}
	}
}

// A refusal must surface as an error, and say enough to act on: 429 is ntfy's rate limit,
// and a bare "non-2xx" would leave you guessing.
func TestNtfyReportsARefusal(t *testing.T) {
	n, _ := fakeNtfy(t, http.StatusTooManyRequests)

	err := n.Notify(context.Background(), Notification{Body: "hello"})
	if err == nil {
		t.Fatal("Notify succeeded against a 429")
	}
	if !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limit reached") {
		t.Errorf("error should carry the status and the server's reason, got: %v", err)
	}
}

// The caller's context governs. Nothing here waits on a notification, so a cancelled one
// must fail fast rather than hold a goroutine open.
func TestNtfyHonoursACancelledContext(t *testing.T) {
	n, _ := fakeNtfy(t, http.StatusOK)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := n.Notify(ctx, Notification{Body: "hello"}); err == nil {
		t.Fatal("Notify succeeded on a cancelled context")
	}
}

// Nop is what an unconfigured server holds. It must be a usable Notifier, not a nil that
// every call site has to check.
func TestNopSwallowsEverything(t *testing.T) {
	var to Notifier = Nop{}
	if err := to.Notify(context.Background(), Notification{Body: "hello"}); err != nil {
		t.Fatalf("Nop.Notify: %v", err)
	}
}

func TestFromEnvIsOffWithoutATopic(t *testing.T) {
	t.Setenv("NTFY_TOPIC", "")

	to, on := FromEnv()
	if on {
		t.Error("FromEnv reported on with no topic set")
	}
	if _, isNop := to.(Nop); !isNop {
		t.Errorf("FromEnv returned %T, want a usable Nop", to)
	}
}

func TestFromEnvBuildsAnNtfyFromTheTopic(t *testing.T) {
	t.Setenv("NTFY_TOPIC", "secret-topic")
	t.Setenv("NTFY_SERVER", "https://ntfy.example.test/")

	to, on := FromEnv()
	if !on {
		t.Fatal("FromEnv reported off with a topic set")
	}
	n, ok := to.(*Ntfy)
	if !ok {
		t.Fatalf("FromEnv returned %T, want *Ntfy", to)
	}
	// The trailing slash has to go, or every publish URL ends up with a "//".
	if n.Server != "https://ntfy.example.test" {
		t.Errorf("Server = %q, want the trailing slash trimmed", n.Server)
	}
	// The topic is a secret; it must not leak into the log line the server writes.
	if strings.Contains(n.String(), "secret-topic") {
		t.Errorf("String() leaks the topic: %q", n.String())
	}
}

// Multi must try every transport, not stop at the first failure — one dead sender silencing
// the others is exactly the outage a Multi exists to survive.
func TestMultiTriesEveryNotifier(t *testing.T) {
	var reached int
	counter := notifierFunc(func() error { reached++; return nil })
	broken := notifierFunc(func() error { reached++; return io.EOF })

	err := Multi{broken, counter, counter}.Notify(context.Background(), Notification{Body: "x"})
	if err == nil {
		t.Error("Multi hid a failure")
	}
	if reached != 3 {
		t.Errorf("reached %d notifiers, want all 3", reached)
	}
}

type notifierFunc func() error

func (f notifierFunc) Notify(context.Context, Notification) error { return f() }
