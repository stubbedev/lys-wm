package compositor

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/loop"
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
	c.spawnWith("true", nil)
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
