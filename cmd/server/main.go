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

// defaultAddr honours $PORT. Container hosts pick the port themselves and inject it —
// bind anything else and the platform's health check never gets an answer, so the deploy
// is marked failed. Locally $PORT is unset and this is just :8080.
//
// ⚠️ This is also why the two demos below share one server behind an /api/{cluster}/
// prefix rather than getting a port each: the host exposes exactly the one port it
// injected. A second http.Server on :8081 would work on the laptop and be unreachable in
// the container.
func defaultAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

// demoClusters are the dashboard's tabs — one wholly independent cluster each.
//
// They need separate clusters because they want the ring in states that cannot both be
// true: the CAP demo leaves the network cut for minutes at a time while writing to both
// sides, which reads as a broken cluster to anyone on the replication tab, and a node
// killed over there would quietly corrupt a run over here.
//
// This costs nothing structural (docs/HLD.md §4). Nodes bind 127.0.0.1:0, so the OS hands
// out every port and two clusters cannot collide — nobody picked a number to collide
// over. And Cluster keeps all its state in its own fields; there is no package-level
// mutable state in cluster/, node/ or cache/ for a second cluster to reach. Isolation
// here is structural, not disciplined.
var demoClusters = []string{"replication", "cap"}

func run() error {
	addr := flag.String("addr", defaultAddr(), "API listen address ($PORT wins by default)")
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

	// Registered before the loop and iterating the map, so a cluster that dies half-way
	// through Start still gets the nodes it did start closed — Close stops whatever is in
	// c.nodes. (The single-cluster version returned on a Start error before its defer was
	// installed, and leaked them.)
	clusters := make(map[string]*cluster.Cluster, len(demoClusters))
	defer func() {
		for _, c := range clusters {
			c.Close()
		}
	}()

	for _, id := range demoClusters {
		c := cluster.New(3, 1, *grace, "n0", "n1", "n2", "n3", "n4")
		// Tag every line this cluster logs. Both clusters name their nodes n0..n4, so
		// without this the log is two interleaved stories about the same five names.
		// Before Start, so the nodes' own startup logs are captured and tagged too.
		c.SetLogger(log.With("cluster", id))
		clusters[id] = c // before Start: a partial failure still has to be closed
		if err := c.Start(); err != nil {
			return fmt.Errorf("start cluster %q: %w", id, err)
		}
		if err := c.Seed(*seed); err != nil {
			// Not fatal: the cluster is up and usable, its ring just starts empty.
			log.Warn("seeding failed; cluster is up but its ring starts empty", "cluster", id, "err", err)
		}
	}

	srv := &http.Server{Addr: *addr, Handler: routes(clusters, log)}

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
