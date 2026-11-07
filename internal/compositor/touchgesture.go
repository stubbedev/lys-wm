package compositor

import (
	"errors"
	"image"
	"time"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const MaxGestureTouches = 10

type touchGesture struct {
	recognizer binds.Recognizer
	binds      []binds.GestureBind
	ids        [MaxGestureTouches]int32
	n          int
	origin     point
	centroid   point
	area       image.Rectangle
	inhibited  bool
	claimed    bool
	horizontal bool
	drive      driven
	workspaces workspaceDrive
}

type touchCentroid struct{}

func (touchCentroid) at(c *Compositor) point { return c.touchGesture.centroid }

func (tg *touchGesture) holds(id int32) bool {
	return tg.claimed && tg.index(id) >= 0
}

func (tg *touchGesture) index(id int32) int {
	for i, t := range tg.ids[:tg.n] {
		if t == id {
			return i
		}
	}
	return -1
}

func atEdge(area image.Rectangle, p point, e binds.Edge, width int) bool {
	w := float64(width)
	switch e {
	case binds.EdgeTop:
		return p.Y < float64(area.Min.Y)+w
	case binds.EdgeBottom:
		return p.Y >= float64(area.Max.Y)-w
	case binds.EdgeLeft:
		return p.X < float64(area.Min.X)+w
	case binds.EdgeRight:
		return p.X >= float64(area.Max.X)-w
	}
	return false
}

func (c *Compositor) touchCentroid() point {
	tg := &c.touchGesture
	var sum point
	for _, id := range tg.ids[:tg.n] {
		at := c.touches[id].at
		sum.X, sum.Y = sum.X+at.X, sum.Y+at.Y
	}
	n := float64(max(tg.n, 1))
	return point{sum.X / n, sum.Y / n}
}

func (c *Compositor) touchGestureAdd(id int32, area image.Rectangle, p point, w *window) {
	tg := &c.touchGesture
	if tg.n == len(tg.ids) {
		return
	}
	first := tg.n == 0
	if first {
		tg.area = area
		tg.inhibited = false
		if w != nil {
			if g := c.decision(w).TouchGestures; g != nil && !*g {
				tg.inhibited = true
			}
		}
	}
	tg.ids[tg.n] = id
	tg.n++
	if tg.claimed {
		tg.centroid = c.touchCentroid()
		tg.origin = tg.centroid
		return
	}
	c.recognizeTouches(first, p)
}

func (c *Compositor) recognizeTouches(fresh bool, first point) {
	tg := &c.touchGesture
	tg.centroid = c.touchCentroid()
	tg.origin = tg.centroid
	tg.binds = tg.binds[:0]
	if !tg.inhibited {
		for _, b := range c.gestures.Touch {
			edge := b.Edge != binds.NoEdge
			if (edge && tg.n == 1 && fresh && atEdge(tg.area, first, b.Edge, c.gestures.EdgeWidth)) || (!edge && int(b.Fingers) == tg.n) {
				tg.binds = append(tg.binds, b)
			}
		}
	}
	tg.recognizer.Begin(tg.binds, backend.Swipe, uint32(tg.n), !c.shell.Locked() && c.inhibiting == nil) //nolint:gosec // at most MaxGestureTouches
}

func (c *Compositor) touchGestureMoved(id int32, at time.Duration) (bool, error) {
	tg := &c.touchGesture
	if tg.index(id) < 0 {
		return false, nil
	}
	tg.centroid = c.touchCentroid()
	dx, dy := tg.centroid.X-tg.origin.X, tg.centroid.Y-tg.origin.Y
	tg.origin = tg.centroid
	wasClaimed := tg.claimed
	step := tg.recognizer.Swipe(dx, dy)
	if step.Forward && !wasClaimed {
		return false, nil
	}
	var err error
	if step.Claimed {
		err = c.claimTouches(step.Bind, at)
	}
	if step.Fire {
		c.run(step.Bind.Action, nil, wm.FocusOther)
	}
	if tg.drive == nil {
		return true, err
	}
	_, window := tg.drive.(*windowDrive)
	if step.Travel == 0 && !window {
		return true, err
	}
	extent := tg.area.Dy()
	if tg.horizontal {
		extent = tg.area.Dx()
	}
	return true, errors.Join(err, tg.drive.moved(step.Travel/float64(max(extent, 1)), at))
}

func (c *Compositor) claimTouches(b binds.GestureBind, at time.Duration) error {
	tg := &c.touchGesture
	tg.claimed, tg.horizontal = true, b.Direction.Horizontal()
	cancelled := c.seat.CancelTouches(tg.ids[:tg.n])
	src := gestureSource{o: c.outputAt(tg.centroid), at: tg.centroid, natural: true, by: touchCentroid{}}
	var err error
	tg.drive, err = c.claimGesture(b, at, src, &tg.workspaces)
	return errors.Join(cancelled, err)
}

func (c *Compositor) touchGestureUp(id int32, at time.Duration) (bool, error) {
	tg := &c.touchGesture
	i := tg.index(id)
	if i < 0 {
		return false, nil
	}
	copy(tg.ids[i:tg.n], tg.ids[i+1:tg.n])
	tg.n--
	claimed := tg.claimed
	switch {
	case tg.n == 0:
		return claimed, c.endTouchGesture(at, false)
	case claimed:
		tg.centroid = c.touchCentroid()
		tg.origin = tg.centroid
	default:
		c.recognizeTouches(false, point{})
	}
	return claimed, nil
}

func (c *Compositor) endTouchGesture(at time.Duration, cancelled bool) error {
	tg := &c.touchGesture
	drive := tg.drive
	tg.drive, tg.claimed, tg.n = nil, false, 0
	tg.recognizer.End(cancelled)
	if drive == nil {
		return nil
	}
	return drive.released(at, cancelled)
}
