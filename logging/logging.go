// Package logging wires the server's logs to two destinations at once: text on the
// console for a human, JSON on disk for grep/jq. Both see every record.
//
// This is the only package that owns a logger; cluster and node take one via SetLogger
// and default to discarding, so a library never logs on its own terms.
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
	// File is the log file path; "" disables file logging (console only). Parent dirs
	// are created as needed. Appended to, never truncated: a restart must not erase
	// the run that explains why you restarted.
	File string

	// Level is debug | info | warn | error. Below it, records are dropped.
	Level string

	// AddSource attaches file:line of the call site to every record.
	AddSource bool
}

// Setup builds the logger, installs it as the process-wide default, and returns it
// along with a close func for the log file.
//
// slog.SetDefault also redirects the standard log package through this handler, so a
// stray log.Printf (including net/http's own panic logs) lands in the file too.
func Setup(opts Options) (logger *slog.Logger, closeFn func() error, err error) {
	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, nil, err
	}

	handlerOpts := &slog.HandlerOptions{Level: level, AddSource: opts.AddSource}

	handlers := []slog.Handler{slog.NewTextHandler(os.Stdout, handlerOpts)}
	closeFn = func() error { return nil }

	if opts.File != "" {
		if dir := filepath.Dir(opts.File); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, nil, fmt.Errorf("create log dir %s: %w", dir, err)
			}
		}
		// O_APPEND is what keeps concurrent writes from interleaving mid-line: the OS
		// positions each write at the current end. Without it, two goroutines can write
		// at the same offset.
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

// multiHandler fans one record out to several handlers. io.MultiWriter cannot do this:
// it duplicates bytes *after* formatting, so both destinations would get one format.
// Fanning out at the Handler level formats each record once per destination.
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
		// Clone per handler: a handler may append to the Record's attr slice, and the
		// first would then mutate what the second sees.
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs and WithGroup must return a *new* multiHandler: slog treats handlers as
// immutable, and mutating in place would change what every other holder sees.
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

// Discard is a logger that throws everything away. The default for cluster and node,
// so a test that never wires a logger stays silent.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

var _ slog.Handler = (*multiHandler)(nil)
