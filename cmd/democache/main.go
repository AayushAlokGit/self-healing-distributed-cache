// Command democache runs the whole cluster-in-a-box behind a dashboard: N cache
// nodes as goroutines in one process, an HTTP control API, and the static UI that
// visualizes the ring and drives the failure-injection demo.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/AayushAlokGit/self-healing-distributed-cache/cluster"
)

func main() {
	addr := flag.String("addr", ":8080", "dashboard listen address")
	grace := flag.Duration("grace", 2*time.Second, "heal grace period (wait before re-replicating a suspected death)")
	seed := flag.Int("seed", 12, "keys to seed at startup (kept small so the ring stays legible)")
	flag.Parse()

	c := cluster.New(3, 1, *grace, "n0", "n1", "n2", "n3", "n4")
	if err := c.Start(); err != nil {
		log.Fatalf("start cluster: %v", err)
	}
	defer c.Close()
	if err := c.Seed(*seed); err != nil {
		log.Printf("seed: %v", err)
	}

	srv := &http.Server{Addr: *addr, Handler: routes(c)}

	// Serve until an interrupt, then drain: stop the users, then stop the cluster.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		log.Printf("dashboard on http://localhost%s  (R=3, grace=%v, %d keys seeded)", *addr, *grace, *seed)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
