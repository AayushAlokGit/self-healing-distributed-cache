// Package logging wires the server's logs to two destinations at once: a
// human-readable stream on the console (stdout) and a machine-readable JSON file
// on disk. Both see every record; neither is a copy made after the fact.
//
// Why two formats rather than one stream teed into two files: the two readers are
// different. A human watching a demo wants short aligned text scrolling by; a
// human debugging afterwards wants to `grep`/`jq` the file for one node's heals.
// Formatting once and duplicating the bytes would force one of them to lose.
//
// This is the only package that owns a logger. cluster and node take one via
// SetLogger and default to discarding — a library that logs on its own terms is a
// library you cannot silence (and it would spray heartbeat noise through
// `go test`).
package logging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Options configures Setup.
type Options struct {
	// File is the log file path. A "" disables file logging (console only).
	// Parent directories are created as needed. The file is appended to, never
	// truncated: a restart must not erase the run that explains why you restarted.
	File string

	// Level is debug | info | warn | error. Below it, records are dropped.
	Level string

	// AddSource attaches file:line of the call site to every record. Useful when
	// hunting; noisy otherwise.
	AddSource bool
}

// Setup builds the logger, installs it as the process-wide default, and returns it
// along with a close func for the log file.
//
// slog.SetDefault does more than set slog's own default: it also redirects the
// *standard* log package ("log".Printf and friends) through this handler. So any
// stray log.Printf — including ones inside net/http, which logs its own panics —
// lands in the file too, instead of vanishing to stderr in a format nothing else
// can parse.
func Setup(opts Options) (logger *slog.Logger, closeFn func() error, err error) {
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, nil, err
	}

	handlerOpts := &slog.HandlerOptions{Level: level, AddSource: opts.AddSource}

	// Console: text, for a human watching the demo.
	handlers := []slog.Handler{slog.NewTextHandler(os.Stdout, handlerOpts)}
	closeFn = func() error { return nil }

	// File: JSON, for a human (or a tool) reading it back.
	if opts.File != "" {
		if dir := filepath.Dir(opts.File); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, nil, fmt.Errorf("create log dir %s: %w", dir, err)
			}
		}
		// O_APPEND is what makes concurrent writes safe to *this* file: each write
		// is positioned at the current end by the OS, so records never interleave
		// mid-line. Without it two goroutines could write at the same offset.
		f, err := os.OpenFile(opts.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file %s: %w", opts.File, err)
		}
		handlers = append(handlers, slog.NewJSONHandler(f, handlerOpts))
		closeFn = f.Close
	}

	logger = slog.New(&multiHandler{handlers: handlers})
	slog.SetDefault(logger)
	return logger, closeFn, nil
}

// multiHandler fans one record out to several handlers. slog ships handlers for
// text and JSON but nothing that writes both, and io.MultiWriter cannot do it —
// that duplicates *bytes*, after formatting, so both destinations would get the
// same format. Fanning out at the Handler level formats each record twice, once
// per destination, which is the whole point.
type multiHandler struct{ handlers []slog.Handler }

// Enabled reports whether *any* destination wants this level, so a record bound
// for the file is not dropped just because the console would ignore it.
func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		// Clone per handler: a Record holds its attrs in a slice that a handler may
		// append to, and handing the same one to two handlers would let the first
		// mutate what the second sees.
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs and WithGroup must push the change into every child handler and return
// a *new* multiHandler — slog treats handlers as immutable, and logger.With(...)
// on a shared logger would otherwise mutate what every other holder sees.
func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: next}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: next}
}

// parseLevel maps the flag's string onto an slog level.
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug|info|warn|error)", s)
	}
}

// Discard is a logger that throws everything away. The default for cluster and
// node, so a test that never wires a logger stays silent.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// compile-time check that multiHandler really is an slog.Handler: a missing method
// is a build error here, not a confusing one at the slog.New call site.
var _ slog.Handler = (*multiHandler)(nil)
