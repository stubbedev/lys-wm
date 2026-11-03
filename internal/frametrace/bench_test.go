package frametrace

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
)

type benchSync struct{}

func (benchSync) NativeFences() bool     { return false }
func (benchSync) SyncFile() (int, error) { return -1, nil }
func (benchSync) Finish() error          { return nil }

func BenchmarkRecorderFrame(b *testing.B) {
	now := time.Second
	r, err := Create(filepath.Join(b.TempDir(), "trace"), Header{}, benchSync{}, Clock{
		Now:    func() time.Duration { return now },
		Signal: func(int) (time.Duration, bool, error) { return now, true, nil },
	}, func(err error) { b.Error(err) })
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		r.Begin("HEADLESS-1")
		now += time.Millisecond
		r.Submit("HEADLESS-1")
		r.End("HEADLESS-1", true, Cost{})
		now += 15 * time.Millisecond
		r.Presented("HEADLESS-1", backend.Presentation{Time: now, Refresh: 16 * time.Millisecond})
	}
	if err := r.Close(); err != nil {
		b.Fatal(err)
	}
}
