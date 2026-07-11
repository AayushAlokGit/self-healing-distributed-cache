// Command server runs the whole cluster-in-a-box behind a control API: N cache
// nodes as goroutines in one process, plus an HTTP API the React frontend (see
// frontend/) drives to visualize the ring and inject failures.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
	"github.com/AayushAlokGit/self-healing-distributed-cache/logging"
)

// main calls run and reports. os.Exit skips deferred functions, so everything that
// must run on the way out (draining the cluster, closing the log file) lives in run,
// where a plain return unwinds it.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "API listen address")
	grace := flag.Duration("grace", 2*time.Second, "heal grace period (wait before re-replicating a suspected death)")
	seed := flag.Int("seed", 12, "keys to seed at startup (kept small so the ring stays legible)")
	logFile := flag.String("log-file", "logs/server.log", "JSON log file; empty disables file logging (console still logs)")
	logLevel := flag.String("log-level", "info", "log level: debug | info | warn | error")
	logSource := flag.Bool("log-source", false, "attach file:line of the log call site to every record")
	flag.Parse()

	log, closeLogs, err := logging.Setup(logging.Options{
		File:      *logFile,
		Level:     *logLevel,
		AddSource: *logSource,
	})
	if err != nil {
		return fmt.Errorf("set up logging: %w", err)
	}
	defer closeLogs()

	log.Info("starting up",
		"addr", *addr,
		"grace", *grace,
		"seed_keys", *seed,
		"log_file", *logFile,
		"log_level", *logLevel,
		"pid", os.Getpid(),
	)

	c := cluster.New(3, 1, *grace, "n0", "n1", "n2", "n3", "n4")
	c.SetLogger(log) // before Start, so the nodes' own startup logs are captured
	if err := c.Start(); err != nil {
		return fmt.Errorf("start cluster: %w", err)
	}
	defer c.Close()

	if err := c.Seed(*seed); err != nil {
		// Not fatal: the cluster is up and usable, the ring just starts empty.
		log.Warn("seeding failed; cluster is up but the ring starts empty", "err", err)
	}

	srv := &http.Server{Addr: *addr, Handler: routes(c, log)}

	// Serve until an interrupt (or a serve failure), then drain: stop the users, then
	// stop the cluster.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Buffered so the goroutine never blocks when the interrupt path takes the other
	// branch and nobody reads this.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("API listening", "url", "http://localhost"+*addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("API server could not serve", "addr", *addr, "err", err)
			serveErr <- err
			stop() // unblock main so it takes the normal shutdown path
			return
		}
		serveErr <- nil
	}()

	<-ctx.Done()
	log.Info("shutting down")
	drain(srv, log)

	// A failed bind must not exit 0.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve on %s: %w", *addr, err)
	}
	log.Info("shutdown complete")
	return nil
}

// drain gives in-flight requests a moment to finish before the nodes go away.
func drain(srv *http.Server, log *slog.Logger) {
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("API server did not drain within the timeout", "err", err)
		return
	}
	log.Debug("API server drained cleanly")
}
