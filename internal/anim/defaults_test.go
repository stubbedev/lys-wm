package anim

import (
	"testing"
	"time"
)

func progressOf(t Table, e Event) (overshoot float64, nearly time.Duration, settled time.Duration) {
	c := t.Curve(e)
	settled = c.Duration()
	for d := time.Duration(0); d <= settled; d += time.Millisecond {
		v := c.At(d)
		overshoot = max(overshoot, v-1)
		if nearly == 0 && v >= 0.95 {
			nearly = d
		}
	}
	return overshoot, nearly, settled
}

func TestDefaultMotionIsTunedPerEvent(t *testing.T) {
	table := MustResolve(DefaultMotion())
	for _, tc := range []struct {
		event        Event
		maxOvershoot float64
		maxNearly    time.Duration
	}{
		{WindowsMove, 0.01, 230 * time.Millisecond},
		{WindowsIn, 0.05, 200 * time.Millisecond},
		{WindowsOut, 0.005, 160 * time.Millisecond},
		{WindowsMinimize, 0.001, 450 * time.Millisecond},
		{Workspaces, 0.001, 300 * time.Millisecond},
		{LayersIn, 0.002, 200 * time.Millisecond},
		{LayersOut, 0.002, 170 * time.Millisecond},
		{Fade, 0.001, 240 * time.Millisecond},
	} {
		overshoot, nearly, settled := progressOf(table, tc.event)
		if overshoot > tc.maxOvershoot {
			t.Errorf("%s overshoots by %.4f, more than %.4f", tc.event, overshoot, tc.maxOvershoot)
		}
		if nearly > tc.maxNearly {
			t.Errorf("%s takes %v to reach 95%%, more than %v", tc.event, nearly, tc.maxNearly)
		}
		t.Logf("%-16s overshoot %.4f, 95%% at %v, settled at %v", tc.event, overshoot, nearly, settled)
		if settled > time.Second {
			t.Errorf("%s keeps drawing for %v", tc.event, settled)
		}
	}
	if table.Curve(WindowsIn) == table.Curve(WindowsOut) || table.Curve(Workspaces) == table.Curve(WindowsMove) || table.Curve(LayersIn) == table.Curve(WindowsIn) {
		t.Error("events share one curve")
	}
	if s := table.Style(LayersIn); s.Kind != FadeStyle {
		t.Errorf("a layer surface enters with %s, which would slide a full-screen one", s)
	}
}

func TestAConfigsKeysBeatTheDefaultMotionBelowThem(t *testing.T) {
	d := 100 * time.Millisecond
	enabled := true
	merged := Over(map[Event]Setting{Windows: {Shape: Linear, Duration: &d}, Global: {Enabled: &enabled}}, DefaultMotion())
	if merged[WindowsIn].Shape != nil || merged[WindowsMove].Shape != nil {
		t.Errorf("a curve on windows does not reach what is below it: %+v", merged[WindowsIn])
	}
	if merged[BorderAngle].Enabled != nil {
		t.Error("enabled on global does not reach borderangle")
	}
	if merged[Workspaces].Shape == nil {
		t.Error("a curve on windows reached workspaces")
	}
	if merged[WindowsIn].Style == nil {
		t.Error("the default style went with the default curve")
	}

	only := Over(map[Event]Setting{Windows: {Duration: &d}}, DefaultMotion())
	table, errs := Resolve(true, only)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if c := table.Curve(WindowsMove); c.Duration() != d {
		t.Errorf("a duration with no curve left %v on windows_move", c.Duration())
	}
	if table.Curve(Workspaces).Duration() == d {
		t.Error("a duration on windows reached workspaces")
	}

	style := Style{Kind: Slide}
	if got := Over(map[Event]Setting{WindowsIn: {Style: &style}}, DefaultMotion())[WindowsIn].Style; got != &style {
		t.Errorf("a config's style lost to the default: %v", got)
	}
}

func TestFrugalMotionDropsOnlyWhatRedrawsAWholeOutput(t *testing.T) {
	table := MustResolve(FrugalMotion())
	if table.Curve(Workspaces) != Instant {
		t.Error("a workspace switch still animates")
	}
	if table.Curve(WindowsMove) == Instant || table.Curve(WindowsIn) == Instant {
		t.Error("window motion went too")
	}
}

func TestKeyTriggeredDefaultsMoveVisiblyOnTheirFirstFrame(t *testing.T) {
	const refresh = time.Second / 60
	table := MustResolve(DefaultMotion())
	for _, e := range []Event{WindowsMove, WindowsIn, WindowsOut, Workspaces, LayersIn, LayersOut, Fade} {
		c := table.Curve(e)
		if first := c.At(refresh); first < 0.075 {
			t.Errorf("%s covers %.1f%% of its travel on the first frame at 60 Hz, want at least 7.5%%", e, first*100)
		}
		half, nearly := 0, 0
		for frame := 1; nearly == 0 && frame <= 60; frame++ {
			v := c.At(time.Duration(frame) * refresh)
			if half == 0 && v >= 0.5 {
				half = frame
			}
			if v >= 0.95 {
				nearly = frame
			}
		}
		if half == 0 || half > 4 || nearly == 0 || nearly > 10 {
			t.Errorf("%s reaches 50%% on frame %d and 95%% on frame %d at 60 Hz, want by 4 and 10", e, half, nearly)
		}
	}
}
