package testenv

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDriversNeverCloseAtTheSameTime(t *testing.T) {
	var closing, overlaps atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := CloseDriver(func() error {
				if closing.Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(time.Millisecond)
				closing.Add(-1)
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := overlaps.Load(); n != 0 {
		t.Fatalf("%d closes overlapped another", n)
	}
}
