package compositor

import (
	"testing"

	"github.com/stubbedev/lys-lib/xcursor"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render/soft"
)

func TestAnExecDoesItsSyscallsOffTheLoop(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	c := &Compositor{loop: l, lookPath: func(string) (string, error) {
		close(entered)
		<-release
		return "/bin/true", nil
	}}
	c.spawnWith("true", nil, func(int) {})
	<-entered
	ticks := 0
	if err := l.Post(func() error {
		ticks++
		l.Stop()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
	if ticks != 1 {
		t.Fatalf("%d loop ticks while the exec's path lookup is stuck", ticks)
	}
	close(release)
	c.execs.Wait()
}

func TestAReloadLoadsItsConfigOffTheLoopAndAppliesItOnTheLoop(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	applied := 0
	c := &Compositor{loop: l}
	c.hooks.Reload = func() func() error {
		close(entered)
		<-release
		return func() error {
			applied++
			l.Stop()
			return nil
		}
	}
	c.ReloadConfig()
	<-entered
	ticks := 0
	if err := l.Post(func() error {
		ticks++
		l.Stop()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
	if ticks != 1 || applied != 0 || !c.ReloadPending() {
		t.Fatalf("%d ticks and %d applies while the load is stuck, pending %t", ticks, applied, c.ReloadPending())
	}
	close(release)
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
	if applied != 1 || c.ReloadPending() {
		t.Fatalf("%d applies once the load finished, pending %t", applied, c.ReloadPending())
	}
}

func TestACursorMissShowsWhatIsThereAndLoadsOffTheLoop(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	c := &Compositor{loop: l, renderer: soft.New(), pointers: map[cursorKey]*cursorTexture{}, report: func(err error) { t.Error(err) }}
	c.cursors.Theme = &xcursor.Theme{}
	c.cursorLoader = func(_ *xcursor.Theme, _ string, px int) (xcursor.Image, error) {
		close(entered)
		<-release
		return xcursor.Builtin(px), nil
	}
	ct, err := c.cursorTextureAt("text", 24)
	if err != nil || ct == nil {
		t.Fatalf("a cursor miss returned %v, %v", ct, err)
	}
	<-entered
	if _, ok := c.pointers[cursorKey{"text", 24}]; ok {
		t.Fatal("the cursor was stored before its load finished")
	}
	c.cursorLoader = nil
	close(release)
	c.offLoopWork.Wait()
	if err := l.Post(func() error {
		l.Stop()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.pointers[cursorKey{"text", 24}]; !ok {
		t.Fatal("the loaded cursor never reached the cache")
	}
}
