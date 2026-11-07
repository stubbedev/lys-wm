package compositor

import (
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/stubbedev/lys-lib/xcursor"
	"golang.org/x/image/draw"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/surface"
)

type cursorKey struct {
	name string
	px   int
}

type cursorTexture struct {
	tex     render.Texture
	px      int
	size    image.Point
	hotspot image.Point
	pixels  []byte
}

var commonCursorShapes = []string{"default", "text", "pointer", "grabbing", "grab", "crosshair", "wait", "ew-resize", "ns-resize", "nwse-resize", "nesw-resize", "not-allowed"}

func loadCursorImage(theme *xcursor.Theme, name string, px int) (xcursor.Image, error) {
	if theme == nil {
		return xcursor.Builtin(px), nil
	}
	img, err := theme.Load(px, xcursor.Names(name)...)
	if err == nil || name == "default" {
		return img, err
	}
	return loadCursorImage(theme, "default", px)
}

func (c *Compositor) cursorTexture(name string, scale output.Scale) (*cursorTexture, error) {
	return c.cursorTextureAt(name, scale.ToDevice(c.cursors.Size))
}

type loadedCursor struct {
	img    xcursor.Image
	size   image.Point
	spot   image.Point
	pixels []byte
}

func prepareCursor(img xcursor.Image, px int) loadedCursor {
	nominal := max(img.Nominal, 1)
	l := loadedCursor{
		img:    img,
		size:   image.Pt(img.Size.X*px/nominal, img.Size.Y*px/nominal),
		spot:   image.Pt(img.Hotspot.X*px/nominal, img.Hotspot.Y*px/nominal),
		pixels: img.Pixels,
	}
	if l.size != img.Size {
		l.pixels = resample(img.Pixels, img.Size, l.size)
	}
	return l
}

func (c *Compositor) storeCursor(key cursorKey, l loadedCursor) (*cursorTexture, error) {
	tex, err := c.renderer.NewTexture(render.ARGB8888, l.img.Size, l.img.Size.X*4, l.img.Pixels)
	if err != nil {
		return nil, fmt.Errorf("compositor: cursor %q at %dpx: %w", key.name, key.px, err)
	}
	ct := &cursorTexture{tex: tex, px: key.px, size: l.size, hotspot: l.spot, pixels: l.pixels}
	c.pointers[key] = ct
	return ct, nil
}

func (c *Compositor) preloadCursors(px int) error {
	if c.cursors.Theme == nil {
		return nil
	}
	for _, name := range commonCursorShapes {
		key := cursorKey{name, px}
		if _, ok := c.pointers[key]; ok {
			continue
		}
		img, err := c.cursorLoader(c.cursors.Theme, name, px)
		if err != nil {
			img = xcursor.Builtin(px)
			c.report(fmt.Errorf("compositor: cursor %q at %dpx: %w", name, px, err))
		}
		if _, err := c.storeCursor(key, prepareCursor(img, px)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Compositor) CursorsLoading() bool {
	return len(c.cursorsLoading) > 0
}

func (c *Compositor) preloadCursorsLater(px int) {
	if c.cursors.Theme == nil {
		return
	}
	for _, name := range commonCursorShapes {
		c.loadCursorLater(cursorKey{name, px})
	}
}

func (c *Compositor) loadCursorLater(key cursorKey) {
	if _, ok := c.pointers[key]; ok {
		return
	}
	if _, ok := c.cursorsLoading[key]; ok {
		return
	}
	if c.cursorsLoading == nil {
		c.cursorsLoading = map[cursorKey]struct{}{}
	}
	c.cursorsLoading[key] = struct{}{}
	theme, load := c.cursors.Theme, c.cursorLoader
	var l loadedCursor
	c.offLoop(func() error {
		img, err := load(theme, key.name, key.px)
		if err != nil {
			img = xcursor.Builtin(key.px)
		}
		l = prepareCursor(img, key.px)
		return err
	}, func(err error) error {
		delete(c.cursorsLoading, key)
		c.landed++
		if err != nil {
			c.report(fmt.Errorf("compositor: cursor %q at %dpx: %w", key.name, key.px, err))
		}
		if _, err := c.storeCursor(key, l); err != nil {
			c.report(err)
			return nil
		}
		c.cursorGen++
		return c.scheduleAll()
	})
}

func (c *Compositor) cursorTextureAt(name string, px int) (*cursorTexture, error) {
	key := cursorKey{name, px}
	if ct, ok := c.pointers[key]; ok {
		return ct, nil
	}
	if c.cursors.Theme == nil {
		return c.storeCursor(key, prepareCursor(xcursor.Builtin(px), px))
	}
	c.loadCursorLater(key)
	if ct := c.nearestCursor(name, px); ct != nil {
		return ct, nil
	}
	return c.storeCursor(cursorKey{builtinCursor, px}, prepareCursor(xcursor.Builtin(px), px))
}

const builtinCursor = "\x00builtin"

func (c *Compositor) nearestCursor(name string, px int) *cursorTexture {
	var best *cursorTexture
	bestKey := cursorKey{}
	better := func(k cursorKey) bool {
		d, b := max(k.px-px, px-k.px), max(bestKey.px-px, px-bestKey.px)
		return best == nil || d < b || d == b && k.px < bestKey.px
	}
	for _, want := range [...]string{name, "default", builtinCursor} {
		for k, ct := range c.pointers {
			if k.name == want && better(k) {
				best, bestKey = ct, k
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

func resample(pix []byte, from, to image.Point) []byte {
	src := &image.RGBA{Pix: pix, Stride: from.X * 4, Rect: image.Rectangle{Max: from}}
	dst := image.NewRGBA(image.Rectangle{Max: to})
	draw.CatmullRom.Scale(dst, dst.Rect, src, src.Rect, draw.Src, nil)
	return dst.Pix
}

func (c *Compositor) drawCursor(pass *frame, o *output.Output, shown []*surface.Surface) ([]*surface.Surface, error) {
	if c.presence.hidden() || !c.seat.HasPointer() || !c.cursor.pixel().In(o.Logical()) {
		return shown, nil
	}
	switch cur := c.seat.Cursor().(type) {
	case seat.SurfaceCursor:
		at := c.cursor.pixel().Sub(cur.Hotspot)
		return c.drawTree(pass, o, cur.Surface, flat(at), shown), nil
	case seat.HiddenCursor:
		return shown, nil
	}
	ct, size, hotspot, err := c.grownCursor(o)
	if err != nil {
		return shown, err
	}
	tip := c.cursorTip(o)
	src := ct.tex.Size()
	pass.DrawTexture(ct.tex, render.FRect{W: float64(src.X), H: float64(src.Y)}, image.Rectangle{Min: tip.Sub(hotspot), Max: tip.Sub(hotspot).Add(size)}, render.Normal, 1)
	return shown, nil
}

func (c *Compositor) grownCursor(o *output.Output) (ct *cursorTexture, size, hotspot image.Point, err error) {
	growth := c.cursorGrowth(c.clock())
	if growth == 1 {
		ct, err = c.cursorTexture(c.cursorShape(), o.Scale())
		if err != nil {
			return nil, image.Point{}, image.Point{}, err
		}
		return ct, ct.size, ct.hotspot, nil
	}
	px := o.Scale().ToDevice(c.cursors.Size)
	if ct, err = c.cursorTextureAt(c.cursorShape(), int(math.Round(float64(px)*shakeGrowth))); err != nil {
		return nil, image.Point{}, image.Point{}, err
	}
	k := growth * float64(px) / float64(ct.px)
	grown := func(p image.Point) image.Point {
		return image.Pt(int(math.Round(float64(p.X)*k)), int(math.Round(float64(p.Y)*k)))
	}
	return ct, grown(ct.size), grown(ct.hotspot), nil
}

func (c *Compositor) cursorTip(o *output.Output) image.Point {
	local := c.cursor.sub(o.Position())
	scale := float64(o.Scale()) / float64(output.Unit)
	return image.Pt(int(math.Floor(local.X*scale)), int(math.Floor(local.Y*scale)))
}

type hwCursor struct {
	plane   backend.CursorPlane
	encoded []byte
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
	growth  float64
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
	if c.shading(o) != nil || c.zoomed(o) || c.composedApart(o) {
		return false, hw.hide()
	}
	ops := &c.cursorOps
	ops.ops = ops.ops[:0]
	var err error
	if c.cursorShown, err = c.drawCursor(ops, o, c.cursorShown[:0]); err != nil {
		return false, err
	}
	if len(ops.ops) == 0 {
		return true, hw.hide()
	}
	box, tip := ops.bounds(), c.cursorTip(o)
	want := cursorImage{gen: c.cursorGen, growth: c.cursorGrowth(c.clock()), scale: o.Scale(), hotspot: tip.Sub(box.Min), size: box.Size()}
	if limit := hw.plane.CursorSize(); want.size.X > limit.X || want.size.Y > limit.Y {
		return false, hw.hide()
	}
	if err := hw.plane.MoveCursor(tip); err != nil {
		return false, c.dropCursorPlane(o, err)
	}
	if !hw.holds || hw.loaded != want {
		err := c.loadCursor(hw, o, box, want.hotspot)
		if errors.Is(err, backend.ErrCursorReadback) {
			return false, hw.hide()
		}
		if err != nil {
			return false, c.dropCursorPlane(o, err)
		}
		hw.loaded, hw.holds = want, true
	}
	return true, nil
}

func (c *Compositor) cursorShape() string {
	if c.picking != nil {
		return "crosshair"
	}
	if c.overviewDragging() {
		return "grabbing"
	}
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
	if c.cursorGrowth(c.clock()) != 1 {
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
	if op.surface != cur.Surface || op.space != nil || op.transform != render.Normal || op.alpha != 1 || op.src != full || op.dst.Size() != cp.size {
		return nil, false
	}
	return cp, true
}

func (c *Compositor) loadCursor(hw *hwCursor, o *output.Output, box image.Rectangle, hotspot image.Point) error {
	plan := c.cursorPlan(o)
	if ct, ok := c.themePixels(o); ok {
		return hw.load(plan, ct.pixels, ct.size.X*4, ct.size, hotspot)
	}
	if cp, ok := c.surfacePixels(); ok {
		return hw.load(plan, cp.pix, cp.size.X*4, cp.size, hotspot)
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
	c.cursorOps.replayFrom(p, origin, colorOut{plans: &c.plans})
	if err := p.End(); err != nil {
		return err
	}
	encodeDrawn(plan, f.Target)
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
	c.cursorOps.replayFrom(p, box.Min, colorOut{plans: &c.plans})
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
		pix, stride, mapErr := r.pending.Map()
		if err = mapErr; err == nil {
			err = hw.load(c.cursorPlan(r.o), pix, stride, r.target.Size(), r.hotspot)
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

func (hw *hwCursor) hide() error {
	hw.holds = false
	return hw.plane.HideCursor()
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
	p := &c.presence
	if p.hidden() && p.pushed {
		return nil
	}
	p.pushed = p.hidden()
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
