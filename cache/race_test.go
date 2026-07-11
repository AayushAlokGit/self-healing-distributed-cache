package cache

import (
	"strconv"
	"sync"
	"testing"
)

// Guards the mutex: without it, `go test -race` reports a DATA RACE and/or the
// runtime aborts with "fatal error: concurrent map writes".
func TestConcurrentSetRace(t *testing.T) {
	c := New(noLimit)
	defer c.Close()

	var wg sync.WaitGroup
	const goroutines = 100
	const writesEach = 100

	// Keys are disjoint across goroutines (k0-*, k1-*, ...). They still race:
	// a map write can grow and rehash the shared backing array.
	for g := range goroutines {
		wg.Go(func() {
			for i := range writesEach {
				c.Set("k"+strconv.Itoa(g)+"-"+strconv.Itoa(i), "v", 0)
			}
		})
	}

	wg.Wait()
}
