package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/notify"
)

// Visit notifications: a push when somebody opens the live dashboard.
//
// The whole difficulty is that a visit is not a request. The dashboard polls /api/state
// about once a second, so a push per request would be a push per second per open tab.
// visits collapses the poll storm back into visits:
//
//   - dedup, on a hash of IP + user-agent, so one visitor is one push;
//   - an IDLE window, refreshed on every poll, so a tab left open all afternoon stays one
//     visit while somebody returning tomorrow is a new one;
//   - a cap per hour, because the API is public and a bot sweeping it must not turn into a
//     denial-of-service on somebody's phone.
type visits struct {
	to  notify.Notifier
	log *slog.Logger

	mu       sync.Mutex
	lastSeen map[string]time.Time
	sent     int       // pushes so far in the current hour
	hour     time.Time // when that hour started
}

const (
	visitWindow   = 30 * time.Minute
	visitsPerHour = 20
	visitForget   = 6 * time.Hour // drop visitors idle this long; the map must not grow forever
	maxTracked    = 10_000        // ...and a hard ceiling, in case a flood outruns visitForget
)

func newVisits(to notify.Notifier, log *slog.Logger, now time.Time) *visits {
	return &visits{to: to, log: log, lastSeen: make(map[string]time.Time), hour: now}
}

// middleware watches the dashboard's own poll. Only GET /api/state: the platform health
// check hits GET /, so gating on the poll route keeps the host's own traffic out.
func (v *visits) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/state" {
			if n, ok := v.visit(r, time.Now()); ok {
				go v.send(n)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// visit reports whether this request begins a new visit, and if so what to say about it.
// It runs on every poll from every tab, so it stays a lock plus a map lookup.
func (v *visits) visit(r *http.Request, now time.Time) (notify.Notification, bool) {
	// Read everything off r HERE. The caller hands the result to a goroutine that outlives
	// the handler, and r is not safe to touch once the handler returns.
	id := visitorID(r)
	body := fmt.Sprintf("%s · %s · %s", id, describeUA(r.UserAgent()), source(r))

	v.mu.Lock()
	defer v.mu.Unlock()

	last, seen := v.lastSeen[id]
	// Refresh on EVERY poll, not only when we push. That makes visitWindow an idle timeout:
	// an open tab is one visit however long it stays open, where a fixed window would push
	// again every 30 minutes at somebody who never left.
	v.lastSeen[id] = now
	if seen && now.Sub(last) < visitWindow {
		return notify.Notification{}, false
	}

	if len(v.lastSeen) > maxTracked {
		v.forgetIdle(now)
		if len(v.lastSeen) > maxTracked {
			clear(v.lastSeen) // abandon dedup rather than grow without bound
		}
	}

	if now.Sub(v.hour) >= time.Hour {
		v.hour, v.sent = now, 0
	}
	if v.sent >= visitsPerHour {
		return notify.Notification{}, false
	}
	v.sent++

	return notify.Notification{
		Title: "Cache demo visit",
		Body:  body,
		Tags:  []string{"eyes"},
	}, true
}

// forgetIdle runs under v.mu.
func (v *visits) forgetIdle(now time.Time) {
	for id, t := range v.lastSeen {
		if now.Sub(t) > visitForget {
			delete(v.lastSeen, id)
		}
	}
}

// send runs in its own goroutine and swallows failure into a log line: a notification is a
// nice-to-have, and the notifier being down must never be visible to the person looking at
// the demo.
func (v *visits) send(n notify.Notification) {
	// context.Background(), NOT the request's. r.Context() is cancelled the instant the
	// response is written, which would cancel this send before it left the process.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := v.to.Notify(ctx, n); err != nil {
		v.log.Warn("visit notification failed", "err", err)
		return
	}
	v.log.Debug("visit notified", "visit", n.Body)
}

// visitorID identifies a visitor without telling the notifier who they are. An ntfy topic
// is readable by anyone who guesses its name, so a raw IP has no business travelling in the
// message: the hash is stable enough to dedup on and says nothing about the person.
func visitorID(r *http.Request) string {
	sum := sha256.Sum256([]byte(clientIP(r) + "|" + r.UserAgent()))
	return hex.EncodeToString(sum[:])[:8]
}

// clientIP prefers X-Forwarded-For: behind a host's proxy, RemoteAddr is the proxy, and
// every visitor would dedup down to the same person. XFF is "client, proxy1, proxy2".
//
// ⚠️ XFF is set by the caller and trivially spoofed — never trust it for anything that
// matters. Here it only feeds dedup, so the worst a spoofer buys is extra pushes, and the
// per-hour cap bounds even that.
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

// source separates a real dashboard visit from somebody curling the API: the dashboard is a
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
