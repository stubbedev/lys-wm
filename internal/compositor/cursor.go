package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"

	"golang.org/x/image/draw"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/xcursor"
)

type cursorKey struct {
	name string
	px   int
}

type cursorTexture struct {
	tex     render.Texture
	size    image.Point
	hotspot image.Point
	pixels  []byte
}

func (c *Compositor) cursorImage(name string, px int) (xcursor.Image, error) {
	if c.cursors.Theme == nil {
		return xcursor.Builtin(px), nil
	}
	img, err := c.cursors.Theme.Load(px, xcursor.Names(name)...)
	if err == nil || name == "default" {
		return img, err
	}
	return c.cursorImage("default", px)
}

func (c *Compositor) cursorTexture(name string, scale output.Scale) (*cursorTexture, error) {
	px := scale.ToDevice(c.cursors.Size)
	key := cursorKey{name, px}
	if ct, ok := c.pointers[key]; ok {
		return ct, nil
	}
	img, err := c.cursorImage(name, px)
	if err != nil {
		return nil, fmt.Errorf("compositor: cursor %q at %dpx: %w", name, px, err)
	}
	tex, err := c.renderer.NewTexture(render.ARGB8888, img.Size, img.Size.X*4, img.Pixels)
	if err != nil {
		return nil, err
	}
	nominal := max(img.Nominal, 1)
	ct := &cursorTexture{
		tex:     tex,
		size:    image.Pt(img.Size.X*px/nominal, img.Size.Y*px/nominal),
		hotspot: image.Pt(img.Hotspot.X*px/nominal, img.Hotspot.Y*px/nominal),
	}
	ct.pixels = img.Pixels
	if ct.size != img.Size {
		ct.pixels = resample(img.Pixels, img.Size, ct.size)
	}
	c.pointers[key] = ct
	return ct, nil
}

func resample(pix []byte, from, to image.Point) []byte {
	src := &image.RGBA{Pix: pix, Stride: from.X * 4, Rect: image.Rectangle{Max: from}}
	dst := image.NewRGBA(image.Rectangle{Max: to})
	draw.CatmullRom.Scale(dst, dst.Rect, src, src.Rect, draw.Src, nil)
	return dst.Pix
}

func (c *Compositor) drawCursor(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	if !c.seat.HasPointer() || !c.cursor.pixel().In(o.Logical()) {
		return shown, nil
	}
	switch cur := c.seat.Cursor().(type) {
	case seat.SurfaceCursor:
		at := c.cursor.pixel().Sub(cur.Hotspot)
		return c.drawTree(pass, o, cur.Surface, flat(at), shown), nil
	case seat.HiddenCursor:
		return shown, nil
	}
	ct, err := c.cursorTexture(c.cursorShape(), o.Scale())
	if err != nil {
		return shown, err
	}
	tip := c.cursorTip(o)
	size := ct.tex.Size()
	pass.DrawTexture(ct.tex, render.FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Min: tip.Sub(ct.hotspot), Max: tip.Sub(ct.hotspot).Add(ct.size)}, render.Normal, 1)
	return shown, nil
}

func (c *Compositor) cursorTip(o *output.Output) image.Point {
	local := c.cursor.sub(o.Position())
	scale := float64(o.Scale()) / float64(output.Unit)
	return image.Pt(int(math.Floor(local.X*scale)), int(math.Floor(local.Y*scale)))
}

type hwCursor struct {
	plane   backend.CursorPlane
	loaded  cursorImage
	holds   bool
	inFrame bool
	target  render.AsyncReadTarget
	reading *cursorRead
}

type cursorRead struct {
	c       *Compositor
	hw      *hwCursor
	o       *output.Output
	target  render.AsyncReadTarget
	pending render.PendingRead
	hotspot image.Point
}

type cursorImage struct {
	gen     uint64
	scale   output.Scale
	hotspot image.Point
	size    image.Point
}

func (c *Compositor) addCursorPlane(o *output.Output) {
	plane, err := o.Backend().CursorPlane()
	switch {
	case err == nil:
		c.hwCursors[o] = &hwCursor{plane: plane}
	case !errors.Is(err, backend.ErrNoCursorPlanes):
		c.report(fmt.Errorf("compositor: drawing the cursor in the frame on %s: %w", o.Backend().Name(), err))
	}
}

func (c *Compositor) placeCursor(o *output.Output) (bool, error) {
	hw, ok := c.hwCursors[o]
	if !ok {
		return false, nil
	}
	ops := &c.cursorOps
	ops.ops = ops.ops[:0]
	var err error
	if c.cursorShown, err = c.drawCursor(ops, o, c.cursorShown[:0]); err != nil {
		return false, err
	}
	if len(ops.ops) == 0 {
		return true, hw.plane.HideCursor()
	}
	box, tip := ops.bounds(), c.cursorTip(o)
	want := cursorImage{gen: c.cursorGen, scale: o.Scale(), hotspot: tip.Sub(box.Min), size: box.Size()}
	if limit := hw.plane.CursorSize(); want.size.X > limit.X || want.size.Y > limit.Y {
		return false, hw.plane.HideCursor()
	}
	if !hw.holds || hw.loaded != want {
		if err := c.loadCursor(hw, o, box, want.hotspot); err != nil {
			return false, c.dropCursorPlane(o, err)
		}
		hw.loaded, hw.holds = want, true
	}
	if err := hw.plane.MoveCursor(tip); err != nil {
		return false, c.dropCursorPlane(o, err)
	}
	return true, nil
}

func (c *Compositor) cursorShape() string {
	if cur, ok := c.seat.Cursor().(seat.ShapeCursor); ok {
		return cur.Shape
	}
	return "default"
}

func (c *Compositor) themePixels(o *output.Output) (*cursorTexture, bool) {
	switch c.seat.Cursor().(type) {
	case seat.SurfaceCursor, seat.HiddenCursor:
		return nil, false
	}
	ct, err := c.cursorTexture(c.cursorShape(), o.Scale())
	return ct, err == nil && ct.pixels != nil
}

type cursorPixels struct {
	pix  []byte
	size image.Point
}

func (c *Compositor) surfacePixels() (*cursorPixels, bool) {
	cur, ok := c.seat.Cursor().(seat.SurfaceCursor)
	if !ok || len(c.cursorOps.ops) != 1 {
		return nil, false
	}
	cp, ok := c.cursorPixels[cur.Surface]
	if !ok {
		return nil, false
	}
	op := c.cursorOps.ops[0]
	full := render.FRect{W: float64(cp.size.X), H: float64(cp.size.Y)}
	if op.surface != cur.Surface || op.transform != render.Normal || op.alpha != 1 || op.src != full || op.dst.Size() != cp.size {
		return nil, false
	}
	return cp, true
}

func (c *Compositor) loadCursor(hw *hwCursor, o *output.Output, box image.Rectangle, hotspot image.Point) error {
	if ct, ok := c.themePixels(o); ok {
		return hw.plane.LoadCursor(ct.pixels, ct.size.X*4, ct.size, hotspot)
	}
	if cp, ok := c.surfacePixels(); ok {
		return hw.plane.LoadCursor(cp.pix, cp.size.X*4, cp.size, hotspot)
	}
	if r, ok := c.asyncReader(); ok {
		return c.readCursor(r, hw, o, box, hotspot)
	}
	origin := box.Min
	f, err := hw.plane.BeginCursor()
	if err != nil {
		return err
	}
	p, err := f.Renderer.Begin(f.Target, render.Region{image.Rectangle{Max: f.Target.Size()}})
	if err != nil {
		return err
	}
	p.Clear(render.Color{})
	c.cursorOps.replayFrom(p, origin)
	if err := p.End(); err != nil {
		return err
	}
	return hw.plane.SetCursor(hotspot)
}

func (c *Compositor) readCursor(r render.AsyncReader, hw *hwCursor, o *output.Output, box image.Rectangle, hotspot image.Point) error {
	size := box.Size()
	target := hw.target
	hw.target = nil
	if target != nil && target.Size() != size {
		if err := target.Destroy(); err != nil {
			return err
		}
		target = nil
	}
	if target == nil {
		var err error
		if target, err = r.NewAsyncTarget(size); err != nil {
			return err
		}
	}
	read := &cursorRead{c: c, hw: hw, o: o, target: target, hotspot: hotspot}
	p, err := c.renderer.Begin(target, render.Region{image.Rectangle{Max: size}})
	if err != nil {
		return errors.Join(err, target.Destroy())
	}
	p.Clear(render.Color{})
	c.cursorOps.replayFrom(p, box.Min)
	if err := p.End(); err != nil {
		return errors.Join(err, target.Destroy())
	}
	if read.pending, err = target.ReadAsync(); err != nil {
		return errors.Join(err, target.Destroy())
	}
	hw.reading = read
	c.cursorReads[read] = struct{}{}
	c.offLoop(read.pending.Wait, read.waited)
	return nil
}

func (r *cursorRead) waited(err error) error {
	c, hw := r.c, r.hw
	current := hw.reading == r && c.hwCursors[r.o] == hw
	if hw.reading == r {
		hw.reading = nil
	}
	if current && err == nil {
		var pix []byte
		if pix, err = r.pending.Map(); err == nil {
			size := r.target.Size()
			err = hw.plane.LoadCursor(pix, size.X*4, size, r.hotspot)
		}
	}
	keep := current && hw.target == nil
	err = errors.Join(err, r.end(keep))
	if keep {
		hw.target = r.target
	}
	if err != nil && current {
		return c.dropCursorPlane(r.o, err)
	}
	return err
}

func (r *cursorRead) end(keepTarget bool) error {
	delete(r.c.cursorReads, r)
	err := r.pending.Release()
	if keepTarget {
		return err
	}
	return errors.Join(err, r.target.Destroy())
}

func (hw *hwCursor) release() error {
	if hw.target == nil {
		return nil
	}
	t := hw.target
	hw.target = nil
	return t.Destroy()
}

func (c *Compositor) dropCursorPlane(o *output.Output, cause error) error {
	hw := c.hwCursors[o]
	delete(c.hwCursors, o)
	c.report(fmt.Errorf("compositor: drawing the cursor in the frame on %s: %w", o.Backend().Name(), cause))
	return errors.Join(hw.plane.HideCursor(), hw.release())
}

func (c *Compositor) cursorMoved() error {
	var err error
	for _, o := range c.outputs {
		placed, placeErr := c.placeCursor(o)
		hw, ok := c.hwCursors[o]
		repaint := !placed || (ok && hw.inFrame)
		if repaint {
			placeErr = errors.Join(placeErr, c.schedule(o))
		}
		err = errors.Join(err, placeErr, c.noteCursor(o, repaint))
	}
	return errors.Join(err, c.updateCursorSessions())
}

func inTree(root, s *surface.Surface) bool {
	for member := range root.Stack() {
		if member == s || (member != root && inTree(member, s)) {
			return true
		}
	}
	return false
}

func (c *Compositor) cursorCommitted(s *surface.Surface) {
	if cur, ok := c.seat.Cursor().(seat.SurfaceCursor); ok && inTree(cur.Surface, s) {
		c.cursorGen++
	}
}

func (c *Compositor) CursorChanged() error {
	c.cursorGen++
	return c.cursorMoved()
}

func (c *Compositor) dropPointers() error {
	c.cursorGen++
	var errs []error
	for _, ct := range c.pointers {
		errs = append(errs, c.destroyTexture(ct.tex))
	}
	clear(c.pointers)
	return errors.Join(errs...)
}
