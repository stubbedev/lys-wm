package anim

import (
	"image"
	"math"
	"testing"
	"time"
)

func TestBezierMatchesKnownValues(t *testing.T) {
	cases := []struct {
		b       Bezier
		x, want float64
	}{
		{Linear, 0.3, 0.3},
		{Ease, 0.5, 0.8024033876},
		{EaseInOut, 0.5, 0.5},
		{EaseIn, 0.25, 0.0934646507},
		{EaseOut, 0.75, 0.9065353493},
	}
	for _, c := range cases {
		if got := c.b.Ease(c.x); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%+v at %v = %v, want %v", c.b, c.x, got, c.want)
		}
	}
	for _, b := range []Bezier{Linear, Ease, EaseIn, EaseOut, EaseInOut} {
		if b.Ease(0) != 0 || b.Ease(1) != 1 || b.Ease(-1) != 0 || b.Ease(2) != 1 {
			t.Errorf("%+v does not run from 0 to 1", b)
		}
		prev := 0.0
		for i := range 1001 {
			v := b.Ease(float64(i) / 1000)
			if v < prev-1e-12 {
				t.Fatalf("%+v falls at %v", b, float64(i)/1000)
			}
			prev = v
		}
	}
}

func TestBezierOvershootsWhenItsControlPointsDo(t *testing.T) {
	b, err := NewBezier(0.05, 0.9, 0.1, 1.1)
	if err != nil {
		t.Fatal(err)
	}
	peak := 0.0
	for i := range 1001 {
		peak = max(peak, b.Ease(float64(i)/1000))
	}
	if peak <= 1 {
		t.Errorf("peak %v, want an overshoot above 1", peak)
	}
}

func TestNewBezierRefusesBadControlPoints(t *testing.T) {
	for _, p := range [][4]float64{{-0.1, 0, 1, 1}, {0, 0, 1.5, 1}, {math.NaN(), 0, 1, 1}, {0, math.Inf(1), 1, 1}} {
		if _, err := NewBezier(p[0], p[1], p[2], p[3]); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
}

func TestSpringsSettleAtTheirTarget(t *testing.T) {
	cases := []struct {
		name                 string
		stiffness, damping   float64
		overshoots           bool
		minSettle, maxSettle time.Duration
	}{
		{"underdamped", 200, 10, true, 500 * time.Millisecond, 3 * time.Second},
		{"critical", 100, 20, false, 500 * time.Millisecond, 3 * time.Second},
		{"overdamped", 100, 60, false, 500 * time.Millisecond, 8 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := NewSpring(c.stiffness, c.damping, 1)
			if err != nil {
				t.Fatal(err)
			}
			if d := s.Duration(); d < c.minSettle || d > c.maxSettle {
				t.Fatalf("settles after %v", d)
			}
			if s.At(0) != 0 || s.At(s.Duration()) != 1 {
				t.Fatalf("runs from %v to %v", s.At(0), s.At(s.Duration()))
			}
			peak := 0.0
			for at := time.Duration(0); at < s.Duration(); at += time.Millisecond {
				peak = max(peak, s.At(at))
			}
			if got := peak > 1+SettleTolerance; got != c.overshoots {
				t.Errorf("peak %v, overshoot %v, want %v", peak, got, c.overshoots)
			}
			if gap := math.Abs(1 - s.At(s.Duration()-time.Millisecond)); gap > 2*SettleTolerance {
				t.Errorf("jumps %v at the settle time", gap)
			}
		})
	}
}

func TestNewSpringRefusesSpringsThatNeverSettle(t *testing.T) {
	for _, p := range [][3]float64{{0, 1, 1}, {1, 0, 1}, {1, 1, -1}, {math.Inf(1), 1, 1}, {1, 1e-6, 1}} {
		if _, err := NewSpring(p[0], p[1], p[2]); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
}

func TestTweensRetargetFromWhatTheyShow(t *testing.T) {
	c, err := Linear.Timed(100*time.Millisecond, true)
	if err != nil {
		t.Fatal(err)
	}
	tw := NewTween(0.0, Lerp)
	tw.Set(100, time.Second, c)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if got := tw.At(time.Second + 25*time.Millisecond); !near(got, 25) {
		t.Fatalf("a quarter in shows %v", got)
	}
	tw.Set(0, time.Second+50*time.Millisecond, c)
	if got := tw.At(time.Second + 50*time.Millisecond); !near(got, 50) {
		t.Fatalf("the retarget starts at %v, want the 50 it showed", got)
	}
	if got := tw.At(time.Second + 100*time.Millisecond); !near(got, 25) {
		t.Fatalf("halfway back shows %v", got)
	}
	if tw.Done(time.Second+149*time.Millisecond) || !tw.Done(time.Second+150*time.Millisecond) || tw.At(time.Hour) != 0 {
		t.Fatal("the retargeted tween does not end at its target on time")
	}
	tw.Jump(7)
	if !tw.Done(0) || tw.At(0) != 7 {
		t.Fatal("a jump is not immediate")
	}
	var zero Tween[float64]
	if !zero.Done(0) || zero.At(0) != 0 {
		t.Fatal("the zero tween is not at rest")
	}
}

func TestStylesParseAndDefaultPerEvent(t *testing.T) {
	for text, want := range map[string]Style{"popin": {Popin, 80}, "popin 60%": {Popin, 60}, "slide": {Slide, 0}} {
		if got, err := ParseStyle(text); err != nil || got != want {
			t.Errorf("%q parsed to %+v, %v", text, got, err)
		}
	}
	for _, text := range []string{"", "popin 60", "popin -1%", "slide left", "spin"} {
		if _, err := ParseStyle(text); err == nil {
			t.Errorf("%q accepted", text)
		}
	}
	if s := (Table{}).Style(WindowsIn); s != (Style{Popin, 80}) {
		t.Errorf("windows_in defaults to %+v", s)
	}
	slide := Style{Kind: Slide}
	table, errs := Resolve(true, map[Event]Setting{WindowsOut: {Style: &slide}, FadeIn: {Style: &slide}})
	if table.Style(WindowsOut) != slide || errs[FadeIn] == nil || errs[WindowsOut] != nil {
		t.Errorf("windows_out %+v, errors %v", table.Style(WindowsOut), errs)
	}
}

func TestLerpRectRoundsAndNeverInverts(t *testing.T) {
	from, to := image.Rect(0, 0, 100, 100), image.Rect(50, 10, 60, 30)
	if got := LerpRect(from, to, 0.5); got != image.Rect(25, 5, 80, 65) {
		t.Errorf("halfway %v", got)
	}
	if got := LerpRect(from, to, 2); got.Dx() < 0 || got.Dy() < 0 {
		t.Errorf("an overshoot inverted the rectangle: %v", got)
	}
}

func TestEventsInheritFromTheirParents(t *testing.T) {
	ms := func(n int) *time.Duration { d := time.Duration(n) * time.Millisecond; return &d }
	off := false
	table, errs := Resolve(true, map[Event]Setting{
		Global:  {Shape: Linear, Duration: ms(400)},
		Windows: {Duration: ms(100)},
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if d := table.Curve(WindowsMove).Duration(); d != 100*time.Millisecond {
		t.Errorf("windows_move takes %v, want the 100ms windows set", d)
	}
	if got := table.Curve(WindowsMove).At(50 * time.Millisecond); got != 0.5 {
		t.Errorf("windows_move inherits a curve at %v halfway, want global's linear", got)
	}
	table, errs = Resolve(true, map[Event]Setting{Windows: {Enabled: &off}})
	if len(errs) != 0 || table.Curve(WindowsMove) != Instant || table.Curve(Global) == Instant {
		t.Errorf("disabling windows: global %v, windows_move %v, errors %v", table.Curve(Global), table.Curve(WindowsMove), errs)
	}
	table, _ = Resolve(false, nil)
	if table.Curve(Global) != Instant {
		t.Error("the master switch off still animates")
	}
	if (Table{}).Curve(WindowsMove) != Instant {
		t.Error("the zero table animates")
	}
	table, errs = Resolve(true, map[Event]Setting{Global: {Duration: ms(300)}, WindowsMove: {Shape: Smooth}})
	if len(errs) != 0 || table.Curve(WindowsMove) != Curve(Smooth) {
		t.Errorf("a spring under an inherited duration: %v, %v", table.Curve(WindowsMove), errs)
	}
	_, errs = Resolve(true, map[Event]Setting{WindowsMove: {Shape: Smooth, Duration: ms(300)}})
	if errs[WindowsMove] == nil {
		t.Error("a spring with its own duration was accepted")
	}
	_, errs = Resolve(true, map[Event]Setting{Windows: {Duration: ms(0)}})
	if errs[Windows] == nil {
		t.Error("a bézier with no duration was accepted")
	}
	for _, e := range Events() {
		if got, ok := EventNamed(e.String()); !ok || got != e {
			t.Errorf("%v does not round-trip its name", e)
		}
	}
}
