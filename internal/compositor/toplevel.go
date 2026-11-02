package compositor

import (
	"image"
	"slices"

	"github.com/stubbedev/lys-wm/internal/output"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type want struct {
	rect      image.Rectangle
	bounds    image.Point
	layer     wm.PlacementLayer
	placed    bool
	activated bool
	minimized bool
	scale     output.Scale
}

type toplevel interface {
	shell.PopupRoot
	Geometry() image.Rectangle
	AppID() string
	Title() string
	Icon() *shell.Icon
	pid() int32
	parent() toplevel
	modal() bool
	prefersFloating() bool
	wantsFullscreen() bool
	sizeLimits() (minimum, maximum image.Point)
	scaled() bool
	settled(w want) bool
	apply(w want) error
	resizes(w want) bool
	mark() answerMark
	answers(u *surface.Update, m answerMark) bool
	synced() bool
	stale() bool
	shownSize() image.Point
	close() error
	ping() error
}

type xdgToplevel struct {
	*shell.Toplevel
}

func (t xdgToplevel) pid() int32 {
	return t.Surface().Client().Creds().Pid
}

func (t xdgToplevel) parent() toplevel {
	if p := t.Parent(); p != nil {
		return xdgToplevel{p}
	}
	return nil
}

func (t xdgToplevel) modal() bool {
	return t.Modal()
}

func (t xdgToplevel) prefersFloating() bool {
	return t.Dialog()
}

func (t xdgToplevel) wantsFullscreen() bool {
	return t.WantsFullscreen()
}

func (t xdgToplevel) sizeLimits() (minimum, maximum image.Point) {
	return t.MinSize(), t.MaxSize()
}

func (t xdgToplevel) scaled() bool {
	return t.Requested().Bounds != (image.Point{})
}

type answerMark struct {
	serial uint32
	size   image.Point
}

var (
	tiledStates = []shell.ToplevelState{
		sxdg.XdgToplevelStateTiledLeft, sxdg.XdgToplevelStateTiledRight, sxdg.XdgToplevelStateTiledTop, sxdg.XdgToplevelStateTiledBottom,
		sxdg.XdgToplevelStateConstrainedLeft, sxdg.XdgToplevelStateConstrainedRight, sxdg.XdgToplevelStateConstrainedTop, sxdg.XdgToplevelStateConstrainedBottom,
	}
	activatedTiledStates      = append(slices.Clone(tiledStates), sxdg.XdgToplevelStateActivated)
	fullscreenStates          = []shell.ToplevelState{sxdg.XdgToplevelStateFullscreen}
	activatedFullscreenStates = []shell.ToplevelState{sxdg.XdgToplevelStateFullscreen, sxdg.XdgToplevelStateActivated}
	activatedStates           = []shell.ToplevelState{sxdg.XdgToplevelStateActivated}
)

func xdgStates(w want) []shell.ToplevelState {
	tiled, fullscreen := w.placed && w.layer == wm.LayerTiled, w.placed && w.layer == wm.LayerFullscreen
	switch {
	case tiled && w.activated:
		return activatedTiledStates
	case tiled:
		return tiledStates
	case fullscreen && w.activated:
		return activatedFullscreenStates
	case fullscreen:
		return fullscreenStates
	case w.activated:
		return activatedStates
	}
	return nil
}

func xdgState(w want) shell.State {
	return shell.State{Size: w.rect.Size(), Bounds: w.bounds, States: xdgStates(w)}
}

func (t xdgToplevel) settled(w want) bool {
	return sameState(t.Requested(), xdgState(w))
}

func (t xdgToplevel) apply(w want) error {
	s := t.Surface()
	if w.scale != 0 {
		if err := s.SetPreferredScale(uint32(w.scale)); err != nil {
			return err
		}
	}
	_, err := t.Configure(xdgState(w))
	return err
}

func (t xdgToplevel) resizes(w want) bool {
	was, size := t.Requested(), w.rect.Size()
	flips := (w.placed && w.layer == wm.LayerFullscreen) != was.Has(sxdg.XdgToplevelStateFullscreen)
	return flips || (size != was.Size && size != t.Geometry().Size())
}

func (t xdgToplevel) mark() answerMark {
	return answerMark{serial: t.LatestSerial()}
}

func (t xdgToplevel) answers(u *surface.Update, m answerMark) bool {
	return t.Acked(m.serial) && u.Buffer() != nil
}

func (xdgToplevel) synced() bool {
	return true
}

func (t xdgToplevel) stale() bool {
	return t.Requested().Size != t.Current().Size
}

func (t xdgToplevel) shownSize() image.Point {
	return t.Geometry().Size()
}

func (t xdgToplevel) close() error {
	return t.SendClose()
}

func (t xdgToplevel) ping() error {
	return t.Ping()
}
