package gpu

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

func currentG() uintptr

type pinnedThread struct {
	g         atomic.Uintptr
	current   atomic.Pointer[Display]
	compiling func()
}

var pinned = struct {
	sync.Mutex
	threads map[uintptr]*pinnedThread
	lookups atomic.Uint64
}{threads: map[uintptr]*pinnedThread{}}

func PinThread() (unpin func() error) {
	runtime.LockOSThread()
	g := currentG()
	pinned.Lock()
	t, nested := pinned.threads[g]
	if !nested {
		t = &pinnedThread{}
		t.g.Store(g)
		pinned.threads[g] = t
	}
	pinned.Unlock()
	return func() error {
		defer runtime.UnlockOSThread()
		if nested {
			return nil
		}
		pinned.Lock()
		delete(pinned.threads, g)
		pinned.Unlock()
		t.g.Store(0)
		d := t.current.Load()
		if d == nil {
			return nil
		}
		return d.Release()
	}
}

func pinnedHere() *pinnedThread {
	g := currentG()
	pinned.Lock()
	defer pinned.Unlock()
	return pinned.threads[g]
}

func CompileThrough(leaveRealtime func()) error {
	t := pinnedHere()
	if t == nil {
		return errors.New("gpu: CompileThrough on a goroutine that pinned no thread")
	}
	t.compiling = leaveRealtime
	return nil
}

func Compiling() {
	if t := pinnedHere(); t != nil && t.compiling != nil {
		t.compiling()
	}
}

func (d *Display) Enter() error {
	runtime.LockOSThread()
	if current, _, _ := purego.SyscallN(d.lib.egl.pGetCurrentContext); current == d.ctx {
		return nil
	}
	if err := d.MakeCurrent(); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	return nil
}

func (d *Display) Leave() error {
	defer runtime.UnlockOSThread()
	g := currentG()
	if t := d.thread.Load(); t != nil && t.g.Load() == g {
		t.current.Store(d)
		return nil
	}
	pinned.lookups.Add(1)
	pinned.Lock()
	t := pinned.threads[g]
	pinned.Unlock()
	if t == nil {
		return d.Release()
	}
	d.thread.Store(t)
	t.current.Store(d)
	return nil
}

func forget(d *Display) {
	pinned.Lock()
	defer pinned.Unlock()
	for _, t := range pinned.threads {
		t.current.CompareAndSwap(d, nil)
	}
}
