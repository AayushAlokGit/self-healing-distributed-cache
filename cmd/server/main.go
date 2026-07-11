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

// main does nothing but call run and report. os.Exit skips deferred functions, so
// anything that must run on the way out (draining the cluster, closing the log
// file) has to live where a plain return unwinds it — not behind an os.Exit.
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

	// Console (text) and file (JSON) both get every record. This also redirects the
	// standard log package through the same handler, so a stray log.Printf — or one
	// from inside net/http — lands in the file too.
	log, closeLogs, err := logging.Setup(logging.Options{
		File:      *logFile,
		Level:     *logLevel,
		AddSource: *logSource,
	})
	if err != nil {
		return fmt.Errorf("set up logging: %w", err)
	}
	defer closeLogs()

	log.Info("self-healing distributed cache starting up",
		"api_addr", *addr,
		"heal_grace", *grace,
		"seed_keys", *seed,
		"log_file", *logFile,
		"log_level", *logLevel,
		"pid", os.Getpid(),
	)

	c := cluster.New(3, 1, *grace, "n0", "n1", "n2", "n3", "n4")
	c.SetLogger(log) // before Start, so the nodes' own startup logs are captured
	if err := c.Start(); err != nil {
		log.Error("cluster failed to start, so there is nothing to serve", "err", err)
		return fmt.Errorf("start cluster: %w", err)
	}
	defer c.Close()

	if err := c.Seed(*seed); err != nil {
		// Not fatal: the cluster is up and usable, the ring just starts empty.
		log.Warn("could not seed the demo keys; the cluster is up but the ring will look empty", "err", err)
	}

	srv := &http.Server{Addr: *addr, Handler: routes(c, log)}

	// Serve until an interrupt (or a serve failure), then drain: stop the users,
	// then stop the cluster.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Buffered so the goroutine never blocks if nobody is reading (the interrupt
	// path takes the other branch), and so main can read the error without racing.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("control API is listening — open the React app (frontend/, npm run dev) or hit /api/state directly",
			"url", "http://localhost"+*addr,
			"replication_factor", 3,
			"heal_grace", *grace,
			"seeded_keys", *seed,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Almost always "address already in use": another instance is running.
			log.Error("the API server could not serve, so the process is giving up", "addr", *addr, "err", err)
			serveErr <- err
			stop() // unblock main so it takes the normal shutdown path
			return
		}
		serveErr <- nil
	}()

	<-ctx.Done()
	log.Info("stopping: draining the API server first so no request is cut off mid-flight, then stopping the nodes")
	drain(srv, log)

	// A failed bind must not exit 0 — CI and the shell need to see it fail.
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serve on %s: %w", *addr, err)
	}
	log.Info("shutdown complete: the API is closed and every node is stopped")
	return nil
}

// drain gives in-flight requests a moment to finish before the nodes go away.
func drain(srv *http.Server, log *slog.Logger) {
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("the API server did not drain within the timeout; shutting down anyway", "err", err)
		return
	}
	log.Info("API server drained cleanly; no request was cut off")
}
