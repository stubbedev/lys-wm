package anim

import (
	"fmt"
	"time"
)

type Event uint8

const (
	Global Event = iota
	Windows
	WindowsMove
	eventCount
)

var events = [eventCount]struct {
	name   string
	parent Event
}{
	Global:      {"global", Global},
	Windows:     {"windows", Global},
	WindowsMove: {"windows_move", Windows},
}

func (e Event) String() string {
	return events[e].name
}

func Events() []Event {
	out := make([]Event, eventCount)
	for i := range out {
		out[i] = Event(i)
	}
	return out
}

func EventNamed(name string) (Event, bool) {
	for e, d := range events {
		if d.name == name {
			return Event(e), true
		}
	}
	return 0, false
}

type Setting struct {
	Enabled  *bool
	Shape    Shape
	Duration *time.Duration
}

type resolved struct {
	enabled  bool
	shape    Shape
	duration time.Duration
	explicit bool
}

var (
	Linear    = Bezier{0, 0, 1, 1}
	Ease      = Bezier{0.25, 0.1, 0.25, 1}
	EaseIn    = Bezier{0.42, 0, 1, 1}
	EaseOut   = Bezier{0, 0, 0.58, 1}
	EaseInOut = Bezier{0.42, 0, 0.58, 1}
	Smooth    = mustSpring(170, 26, 1)
)

func mustSpring(stiffness, damping, mass float64) Spring {
	s, err := NewSpring(stiffness, damping, mass)
	if err != nil {
		panic(err)
	}
	return s
}

func Builtins() map[string]Shape {
	return map[string]Shape{"linear": Linear, "ease": Ease, "ease_in": EaseIn, "ease_out": EaseOut, "ease_in_out": EaseInOut, "spring": Smooth}
}

const DefaultDuration = 250 * time.Millisecond

type Table struct {
	curves [eventCount]Curve
}

func (t Table) Curve(e Event) Curve {
	if c := t.curves[e]; c != nil {
		return c
	}
	return Instant
}

func Resolve(enabled bool, settings map[Event]Setting) (Table, map[Event]error) {
	var t Table
	var got [eventCount]resolved
	errs := map[Event]error{}
	for _, e := range Events() {
		r := resolved{enabled: true, shape: EaseOut, duration: DefaultDuration}
		if e != Global {
			r = got[events[e].parent]
			r.explicit = false
		}
		s := settings[e]
		if s.Enabled != nil {
			r.enabled = *s.Enabled
		}
		if s.Shape != nil {
			r.shape = s.Shape
		}
		if s.Duration != nil {
			r.duration, r.explicit = *s.Duration, true
		}
		got[e] = r
		c, err := r.shape.Timed(r.duration, r.explicit)
		if err != nil {
			errs[e] = fmt.Errorf("event %s: %w", e, err)
			continue
		}
		if enabled && r.enabled {
			t.curves[e] = c
		}
	}
	return t, errs
}
