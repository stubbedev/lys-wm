package binds

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var (
	workspacesBind = GestureBind{Kind: backend.Swipe, Fingers: 3, Direction: SwipeHorizontal, Drive: DriveWorkspaces}
	upBind         = GestureBind{Kind: backend.Swipe, Fingers: 4, Direction: SwipeUp, Action: wm.KillAction{}}
	pinchBind      = GestureBind{Kind: backend.Pinch, Fingers: 3, Direction: PinchIn, Action: wm.KillAction{}}
	holdBind       = GestureBind{Kind: backend.Hold, Fingers: 4, Action: wm.KillAction{}}
	gestureBinds   = []GestureBind{workspacesBind, upBind, pinchBind, holdBind}
)

func TestAnUnboundGestureAllReachesTheClient(t *testing.T) {
	var r Recognizer
	if s := r.Begin(gestureBinds, backend.Swipe, 5, true); !s.Forward || s.Claimed {
		t.Fatalf("begin %+v", s)
	}
	if s := r.Swipe(500, 0); !s.Forward || s.Claimed {
		t.Fatalf("update %+v", s)
	}
	if s := r.End(false); !s.Forward {
		t.Fatalf("end %+v", s)
	}
}

func TestASwipeClaimedOnceItsDirectionIsKnown(t *testing.T) {
	var r Recognizer
	if s := r.Begin(gestureBinds, backend.Swipe, 3, true); !s.Forward {
		t.Fatalf("begin %+v", s)
	}
	if s := r.Swipe(-5, 1); !s.Forward || s.Claimed {
		t.Fatalf("below the lock distance %+v", s)
	}
	s := r.Swipe(-8, 0)
	if s.Forward || !s.Claimed || !s.CancelClient || s.Bind.Drive != DriveWorkspaces || s.Travel != -13 {
		t.Fatalf("at the lock, with all the travel so far: %+v", s)
	}
	if s := r.Swipe(-4, 9); s.Forward || s.Claimed || s.CancelClient || s.Travel != -4 {
		t.Fatalf("after the claim only the bound axis counts: %+v", s)
	}
	if s := r.End(false); s.Forward || s.Fire {
		t.Fatalf("end %+v", s)
	}
}

func TestASwipeOnAnUnboundAxisStaysWithTheClient(t *testing.T) {
	var r Recognizer
	r.Begin(gestureBinds, backend.Swipe, 3, true)
	if s := r.Swipe(1, 20); !s.Forward || s.Claimed {
		t.Fatalf("vertical %+v", s)
	}
	if s := r.Swipe(100, 0); !s.Forward || s.Claimed {
		t.Fatalf("once passed, it never comes back: %+v", s)
	}
	if s := r.End(false); !s.Forward {
		t.Fatalf("end %+v", s)
	}
}

func TestADirectionOnlyMatchesThatDirection(t *testing.T) {
	var r Recognizer
	r.Begin(gestureBinds, backend.Swipe, 4, true)
	if s := r.Swipe(0, 20); s.Claimed {
		t.Fatalf("down claimed an up bind: %+v", s)
	}
	r.Begin(gestureBinds, backend.Swipe, 4, true)
	if s := r.Swipe(0, -20); !s.Claimed || s.Travel != 20 {
		t.Fatalf("up, travel measured along the bound direction: %+v", s)
	}
}

func TestASwipeActionFiresOnceAfterEnoughTravel(t *testing.T) {
	var r Recognizer
	r.Begin(gestureBinds, backend.Swipe, 4, true)
	if s := r.Swipe(0, -20); !s.Claimed || s.Fire {
		t.Fatalf("claimed, not fired: %+v", s)
	}
	if s := r.Swipe(0, -70); s.Fire {
		t.Fatalf("too early: %+v", s)
	}
	if s := r.Swipe(0, -20); !s.Fire {
		t.Fatalf("not fired at 110: %+v", s)
	}
	if s := r.Swipe(0, -200); s.Fire {
		t.Fatalf("fired twice: %+v", s)
	}
}

func TestAPinchClaimsAndFiresAtItsThreshold(t *testing.T) {
	var r Recognizer
	r.Begin(gestureBinds, backend.Pinch, 3, true)
	if s := r.Pinch(0.95); !s.Forward || s.Claimed {
		t.Fatalf("small pinch %+v", s)
	}
	s := r.Pinch(0.8)
	if s.Forward || !s.Claimed || !s.CancelClient || !s.Fire {
		t.Fatalf("at the threshold %+v", s)
	}
	if s := r.Pinch(0.5); s.Forward || s.Fire {
		t.Fatalf("after %+v", s)
	}
	r.Begin(gestureBinds, backend.Pinch, 3, true)
	if s := r.Pinch(1.3); !s.Forward || s.Claimed {
		t.Fatalf("the other way %+v", s)
	}
}

func TestABoundHoldNeverReachesTheClientAndFiresOnAnUncancelledEnd(t *testing.T) {
	var r Recognizer
	if s := r.Begin(gestureBinds, backend.Hold, 4, true); s.Forward || !s.Claimed || s.CancelClient {
		t.Fatalf("begin %+v", s)
	}
	if s := r.End(false); s.Forward || !s.Fire {
		t.Fatalf("end %+v", s)
	}
	r.Begin(gestureBinds, backend.Hold, 4, true)
	if s := r.End(true); s.Forward || s.Fire {
		t.Fatalf("a cancelled hold %+v", s)
	}
}

func TestNothingIsClaimedWhenNotAllowed(t *testing.T) {
	var r Recognizer
	if s := r.Begin(gestureBinds, backend.Hold, 4, false); !s.Forward || s.Claimed {
		t.Fatalf("hold %+v", s)
	}
	r.Begin(gestureBinds, backend.Swipe, 3, false)
	if s := r.Swipe(100, 0); !s.Forward || s.Claimed {
		t.Fatalf("swipe %+v", s)
	}
}

func TestASwallowedGestureForwardsNothingAndCancelsNothing(t *testing.T) {
	var r Recognizer
	r.Begin(gestureBinds, backend.Swipe, 3, true)
	if !r.Drives(DriveWorkspaces) {
		t.Fatal("three fingers can drive the workspaces")
	}
	r.Swallow()
	if s := r.Swipe(1, 0); s.Forward {
		t.Fatalf("before the lock %+v", s)
	}
	if s := r.Swipe(20, 0); !s.Claimed || s.CancelClient {
		t.Fatalf("the client never saw it: %+v", s)
	}
	r.Begin(gestureBinds, backend.Swipe, 3, true)
	r.Swallow()
	if s := r.Swipe(0, 20); s.Forward || s.Claimed || !s.Passed {
		t.Fatalf("the wrong axis %+v", s)
	}
	if s := r.End(false); s.Forward {
		t.Fatalf("end %+v", s)
	}
}

func TestGesturesOutsideASequenceAreForwarded(t *testing.T) {
	var r Recognizer
	if !r.Swipe(1, 1).Forward || !r.Pinch(2).Forward || !r.End(false).Forward {
		t.Fatal("a stray event was swallowed")
	}
}

func TestGestureBindsOverlap(t *testing.T) {
	left := GestureBind{Kind: backend.Swipe, Fingers: 3, Direction: SwipeLeft}
	right := GestureBind{Kind: backend.Swipe, Fingers: 3, Direction: SwipeRight}
	if !workspacesBind.Overlaps(left) || !left.Overlaps(workspacesBind) {
		t.Error("horizontal covers left")
	}
	if left.Overlaps(right) {
		t.Error("left and right are different")
	}
	if left.Overlaps(GestureBind{Kind: backend.Swipe, Fingers: 4, Direction: SwipeLeft}) {
		t.Error("finger counts differ")
	}
	if !holdBind.Overlaps(GestureBind{Kind: backend.Hold, Fingers: 4}) {
		t.Error("the same hold twice")
	}
}

func BenchmarkRecognizerSwipe(b *testing.B) {
	b.ReportAllocs()
	var r Recognizer
	r.Begin(gestureBinds, backend.Swipe, 3, true)
	r.Swipe(100, 0)
	for range b.N {
		if s := r.Swipe(1, 0); s.Forward {
			b.Fatal("a claimed swipe went to the client")
		}
	}
}
