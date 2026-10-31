package anim

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Event uint8

const (
	Global Event = iota
	Windows
	WindowsMove
	WindowsIn
	WindowsOut
	Fade
	FadeIn
	FadeOut
	eventCount
)

type StyleKind uint8

const (
	NoStyle StyleKind = iota
	Popin
	Slide
)

var styleNames = map[StyleKind]string{Popin: "popin", Slide: "slide"}

type Style struct {
	Kind    StyleKind
	Percent float64
}

func (s Style) String() string {
	if s.Kind == Popin {
		return fmt.Sprintf("popin %g%%", s.Percent)
	}
	return styleNames[s.Kind]
}

const defaultPopin = 80

func ParseStyle(text string) (Style, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return Style{}, errors.New("anim: an empty style")
	}
	for kind, name := range styleNames {
		if fields[0] != name {
			continue
		}
		s := Style{Kind: kind}
		switch {
		case kind == Popin && len(fields) == 1:
			s.Percent = defaultPopin
		case kind == Popin && len(fields) == 2:
			v, err := strconv.ParseFloat(strings.TrimSuffix(fields[1], "%"), 64)
			if err != nil || !strings.HasSuffix(fields[1], "%") || !(v >= 0 && v <= 100) {
				return Style{}, fmt.Errorf("anim: popin takes a percentage from 0%% to 100%%, not %q", fields[1])
			}
			s.Percent = v
		case len(fields) > 1:
			return Style{}, fmt.Errorf("anim: %s takes no argument, not %q", name, strings.Join(fields[1:], " "))
		}
		return s, nil
	}
	return Style{}, fmt.Errorf("anim: unknown style %q", fields[0])
}

var events = [eventCount]struct {
	name   string
	parent Event
	styles []StyleKind
	style  Style
}{
	Global:      {name: "global", parent: Global},
	Windows:     {name: "windows", parent: Global},
	WindowsMove: {name: "windows_move", parent: Windows},
	WindowsIn:   {name: "windows_in", parent: Windows, styles: []StyleKind{Popin, Slide}, style: Style{Kind: Popin, Percent: defaultPopin}},
	WindowsOut:  {name: "windows_out", parent: Windows, styles: []StyleKind{Popin, Slide}, style: Style{Kind: Popin, Percent: defaultPopin}},
	Fade:        {name: "fade", parent: Global},
	FadeIn:      {name: "fade_in", parent: Fade},
	FadeOut:     {name: "fade_out", parent: Fade},
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
	Style    *Style
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
	styles [eventCount]Style
}

func (t Table) Style(e Event) Style {
	if s := t.styles[e]; s != (Style{}) {
		return s
	}
	return events[e].style
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
		if st := s.Style; st != nil {
			if !slices.Contains(events[e].styles, st.Kind) {
				errs[e] = fmt.Errorf("event %s takes no style %s", e, st)
				continue
			}
			t.styles[e] = *st
		}
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
