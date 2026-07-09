package cache

import (
	"strconv"
	"sync"
	"testing"
)

// TestConcurrentSetRace fires many goroutines writing to the cache at once.
// With the naive (unsynchronized) map this triggers a data race:
//   - `go test -race` reports "DATA RACE", and/or
//   - Go's runtime aborts with "fatal error: concurrent map writes".
//
// This test EXISTS to demonstrate the failure. Step 2's mutex makes it pass.
func TestConcurrentSetRace(t *testing.T) {
	c := New()

	var wg sync.WaitGroup // waits for all goroutines to finish
	const goroutines = 100
	const writesEach = 100

	// Keys are disjoint across goroutines (k0-*, k1-*, ...). They still race:
	// a map write can grow and rehash the shared backing array.
	for g := range goroutines {
		// wg.Go runs the closure in a new goroutine and registers it with wg,
		// so wg.Wait() below blocks until the closure returns.
		wg.Go(func() {
			for i := range writesEach {
				c.Set("k"+strconv.Itoa(g)+"-"+strconv.Itoa(i), "v")
			}
		})
	}

	wg.Wait() // block until every goroutine has returned
}
