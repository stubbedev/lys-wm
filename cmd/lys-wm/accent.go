package main

import (
	"sync"

	"github.com/stubbedev/lys-ui/appearance"
	"github.com/stubbedev/lys-ui/portalsettings"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
)

type accentFollower struct {
	mu      sync.Mutex
	closed  bool
	monitor *portalsettings.Monitor
}

func followAccent(follow func(render.Color)) *accentFollower {
	f := &accentFollower{}
	go f.start(follow)
	return f
}

func (f *accentFollower) start(follow func(render.Color)) {
	m := portalsettings.New()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		m.Close()
		return
	}
	f.monitor = m
	m.OnAccentChange(func(a appearance.Accent) { follow(desktopAccent(a)) })
	follow(desktopAccent(m.Accent()))
}

func (f *accentFollower) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.monitor != nil {
		f.monitor.Close()
	}
}

func desktopAccent(a appearance.Accent) render.Color {
	c, ok := a.Color()
	if !ok {
		return compositor.DefaultAccent
	}
	return render.Color{R: c.R(), G: c.G(), B: c.B(), A: 0xff}
}
