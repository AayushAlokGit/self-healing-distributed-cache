package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/notify"
)

// faults pushes a notification when somebody injects a fault from the dashboard — the kill
// and cut buttons, and nothing else. Those two are the demo's money moment: everything else
// (a seed, a read, a revive) is somebody warming up.
//
// ⚠️ This is deliberately NOT a middleware, unlike the visit notifier it replaced. Two things
// only the handler knows: WHICH node or which two sides (the ids are in the JSON body, which a
// middleware would have to buffer and re-parse), and whether the action actually HAPPENED —
// `kill n7` on a five-node cluster is a 400, and pushing "n7 killed" for it is a push about
// nothing. So the handlers call this after their error check, on the success path only.
//
// ⚠️ The message carries the clicker's IP, and an ntfy topic has no password — its name is the
// only thing protecting it. Keep it long, random, and out of git, logs, and frontend code.
type faults struct {
	to  notify.Notifier
	log *slog.Logger

	mu   sync.Mutex
	sent int       // pushes so far in the current hour
	hour time.Time // when that hour started
}

// The API is public and a kill is one unauthenticated POST, so a script can hold the button
// down. The cap is what stops that from becoming a denial-of-service on somebody's phone.
// Higher than the visit cap was: these are rarer and each one is worth reading.
const faultsPerHour = 30

func newFaults(to notify.Notifier, log *slog.Logger, now time.Time) *faults {
	return &faults{to: to, log: log, hour: now}
}

// killed and cut are the two call sites, named after the buttons so the wiring in routes()
// reads as what the user did.

func (f *faults) killed(r *http.Request, cluster, node string) {
	f.announce(r, fmt.Sprintf("%s · killed %s", cluster, node), "skull")
}

func (f *faults) cut(r *http.Request, cluster string, sideA, sideB []string) {
	what := fmt.Sprintf("%s · cut the network: [%s] | [%s]",
		cluster, strings.Join(sideA, " "), strings.Join(sideB, " "))
	f.announce(r, what, "zap")
}

// announce builds the notification and hands it to a goroutine, so a slow ntfy never shows up
// as a slow kill button.
//
// ⚠️ Read everything off r HERE. The goroutine below outlives the handler, and r is not safe
// to touch once the handler has returned.
func (f *faults) announce(r *http.Request, what, tag string) {
	n := notify.Notification{
		Title: "Cache demo: fault injected",
		Body:  what + "\n" + clientIP(r) + " · " + describeUA(r.UserAgent()) + " · " + source(r),
		Tags:  []string{tag},
		// Above default: the point of this notifier is that a phone buzzes while somebody is
		// still on the page, not that a line lands in a list to read tomorrow.
		Priority: notify.High,
	}
	if !f.allow(time.Now()) {
		f.log.Warn("fault notification dropped by the hourly cap", "fault", what)
		return
	}
	go f.send(n)
}

// allow reports whether there is budget left this hour, and spends it if so. Split out so the
// cap is a plain unit test — no transport, no goroutine, no clock to wait on.
func (f *faults) allow(now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	if now.Sub(f.hour) >= time.Hour {
		f.hour, f.sent = now, 0
	}
	if f.sent >= faultsPerHour {
		return false
	}
	f.sent++
	return true
}

// send swallows failure into a log line: the notifier being down must never be visible to the
// person looking at the demo.
func (f *faults) send(n notify.Notification) {
	// ⚠️ context.Background(), NOT the request's. r.Context() is cancelled the instant the
	// response is written, which would cancel this send before it left the process.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := f.to.Notify(ctx, n); err != nil {
		f.log.Warn("fault notification failed", "err", err)
		return
	}
	f.log.Debug("fault notified", "fault", n.Body)
}

// clientIP prefers X-Forwarded-For ("client, proxy1, proxy2"): behind a host's proxy,
// RemoteAddr is the proxy, so every visitor would look like the same person.
//
// ⚠️ XFF is set by the caller and trivially spoofed — never trust it for anything that
// matters. Here it only decorates a message, so the worst a spoofer buys is a wrong IP in a
// notification about a kill that really did happen.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // no port to strip
	}
	return host
}

// source separates a real dashboard click from somebody curling the API: the dashboard is a
// different origin in production, so a browser fetch always sends Origin. curl does not.
func source(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		return "from " + o
	}
	return "direct API hit"
}

// describeUA is a deliberately crude sniff — enough to make a phone notification readable,
// not a user-agent parser. ⚠️ Order matters: Edge and Opera both claim "Chrome", and Chrome
// claims "Safari", so the most specific string has to be tested first.
func describeUA(ua string) string {
	if ua == "" {
		return "unknown client"
	}
	pick := func(pairs [][2]string, fallback string) string {
		for _, p := range pairs {
			if strings.Contains(ua, p[0]) {
				return p[1]
			}
		}
		return fallback
	}
	browser := pick([][2]string{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"},
		{"Chrome/", "Chrome"}, {"Safari/", "Safari"}, {"curl/", "curl"},
	}, "unknown browser")
	os := pick([][2]string{
		{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"},
		{"Windows", "Windows"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"},
	}, "unknown OS")
	return browser + " on " + os
}
