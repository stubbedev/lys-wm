package compositor

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shm"
	"github.com/stubbedev/lys-wm/internal/surface"
)

var (
	captureFormats       = []render.Format{render.XRGB8888, render.ARGB8888}
	dmabufCaptureFormats = []fourcc.Format{fourcc.XRGB8888, fourcc.ARGB8888}
)

type captureRenderer interface {
	ImportTarget(b gpu.DMABUF) (render.ReadTarget, error)
	RenderableModifiers(format fourcc.Format) ([]fourcc.Modifier, error)
	MainDevice() (uint64, error)
	Finish() error
	NativeFences() bool
	SyncFile() (int, error)
}

var opaque = render.Color{A: 0xff}

type captureTarget interface {
	fill(c *Compositor, k *capture, finish func(error) error) error
}

type shmCapture struct {
	b       *shm.Buffer
	scratch *captureScratch
}

func (t shmCapture) fill(c *Compositor, k *capture, finish func(error) error) error {
	if r, ok := c.asyncReader(); ok {
		return c.readAsync(r, t.b, t.scratch, k, finish)
	}
	return finish(t.read(c, k))
}

func (t shmCapture) read(c *Compositor, k *capture) (err error) {
	c.shmSyncReads++
	target, err := c.renderer.NewTarget(k.size)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, target.Destroy()) }()
	update := k.updated()
	if err := c.paintRegion(target, k, update); err != nil {
		return err
	}
	stride := k.size.X * 4
	if k.update == nil {
		return t.b.Write(func(data []byte, dstStride int) error {
			c.copiedBytes.Add(uint64(stride * k.size.Y)) //nolint:gosec // a capture size is positive
			return target.Read(data, dstStride)
		})
	}
	t.scratch.pix = slices.Grow(t.scratch.pix[:0], stride*k.size.Y)[:stride*k.size.Y]
	if err := target.Read(t.scratch.pix, stride); err != nil {
		return err
	}
	return t.b.Write(func(data []byte, dstStride int) error {
		c.copiedBytes.Add(copyRegion(data, dstStride, t.scratch.pix, stride, update, func() bool { return false }))
		return nil
	})
}

type dmabufCapture struct {
	b *dmabuf.Buffer
	r captureRenderer
}

func (t dmabufCapture) fill(c *Compositor, k *capture, finish func(error) error) error {
	target, err := c.captureTarget(t.r, t.b)
	if err == nil {
		err = c.paintRegion(target, k, k.updated())
	}
	if err != nil || !t.r.NativeFences() {
		return finish(errors.Join(err, t.r.Finish()))
	}
	fd, err := t.r.SyncFile()
	if err != nil || fd < 0 {
		return finish(err)
	}
	return c.loop.Once(fd, func() error { return finish(nil) })
}

func (c *Compositor) captureTarget(r captureRenderer, b *dmabuf.Buffer) (render.ReadTarget, error) {
	if t, ok := c.captureTargets[b]; ok {
		return t, nil
	}
	t, err := r.ImportTarget(b.DMABUF())
	if err != nil {
		return nil, err
	}
	c.captureTargets[b] = t
	return t, nil
}

func (c *Compositor) paintRegion(target render.Target, k *capture, repaint render.Region) error {
	c.capturedPixels += uint64(repaint.Area()) //nolint:gosec // an area is not negative
	p, err := c.renderer.Begin(target, repaint)
	if err != nil {
		return err
	}
	p.Clear(k.clear)
	return errors.Join(k.draw(p), p.End())
}

func (k *capture) updated() render.Region {
	if k.update == nil {
		return render.Region{image.Rectangle{Max: k.size}}
	}
	return k.update
}

func copyRegion(dst []byte, dstStride int, src []byte, stride int, update render.Region, cancelled func() bool) uint64 {
	var n uint64
	for _, r := range update {
		from, to := r.Min.X*4, r.Max.X*4
		for y := r.Min.Y; y < r.Max.Y; y++ {
			if cancelled() {
				return n
			}
			n += uint64(copy(dst[y*dstStride+from:y*dstStride+to], src[y*stride+from:])) //nolint:gosec // a copy's length is not negative
		}
	}
	return n
}

type capture struct {
	output  *output.Output
	size    image.Point
	clear   render.Color
	draw    func(render.Pass) error
	target  captureTarget
	waits   func(damage *region.Set) bool
	painted func() error
	done    func(at time.Duration, err error) error
	read    *asyncRead
	partial bool
	repaint render.Region
	update  render.Region
}

type frameState uint8

const (
	frameIdle frameState = iota
	frameCapturing
	frameDone
	frameGone
)

type frameLife struct {
	state   frameState
	pending *capture
}

func (l *frameLife) start(k *capture) {
	l.state, l.pending = frameCapturing, k
}

func (l *frameLife) finish() (alive bool) {
	l.pending = nil
	if l.state == frameGone {
		return false
	}
	l.state = frameDone
	return true
}

func (l *frameLife) end(c *Compositor) {
	l.state = frameGone
	if l.pending != nil {
		c.cancelCapture(l.pending)
		l.pending = nil
	}
}

func (c *Compositor) queueCapture(k *capture, waitForDamage bool) error {
	if !c.outputLive(k.output) {
		return k.done(0, errOutputGone(k.output))
	}
	if c.sceneOf(k.output).replayable() && !waitForDamage {
		return c.runCapture(k)
	}
	c.captures[k.output] = append(c.captures[k.output], k)
	return c.schedule(k.output)
}

func (c *Compositor) runCaptures(o *output.Output) error {
	sc := c.sceneOf(o)
	for _, s := range c.captureSessions {
		s.noteDamage(o, &sc.damage, sc.cursorOnly)
	}
	return c.flushCaptures(o, &sc.damage)
}

func (c *Compositor) wakeCaptures(o *output.Output) error {
	if !c.outputLive(o) || !c.sceneOf(o).replayable() {
		return nil
	}
	return c.flushCaptures(o, &region.Set{})
}

func (c *Compositor) flushCaptures(o *output.Output, damage *region.Set) error {
	pending := c.captures[o]
	if len(pending) == 0 {
		return nil
	}
	delete(c.captures, o)
	errs := make([]error, 0, len(pending))
	for _, k := range pending {
		if k.waits != nil && k.waits(damage) {
			c.captures[o] = append(c.captures[o], k)
			continue
		}
		errs = append(errs, c.runCapture(k))
	}
	return errors.Join(errs...)
}

func addDamage(dst, frame *region.Set, area image.Rectangle) {
	for _, r := range frame.Rects() {
		if r = r.Intersect(area); !r.Empty() {
			dst.Add(r.Sub(area.Min))
		}
	}
}

func (c *Compositor) dropCaptures(o *output.Output) error {
	pending := c.captures[o]
	delete(c.captures, o)
	errs := make([]error, 0, len(pending))
	for _, k := range pending {
		errs = append(errs, k.done(0, errOutputGone(o)))
	}
	return errors.Join(errs...)
}

var errCaptureGone = errors.New("the capture's source or frame went away")

func errOutputGone(o *output.Output) error {
	return fmt.Errorf("compositor: %s: %w", o.Backend().Name(), errCaptureGone)
}

func (c *Compositor) cancelCapture(k *capture) {
	if k.read != nil {
		k.read.cancel()
	}
	pending := slices.DeleteFunc(c.captures[k.output], func(x *capture) bool { return x == k })
	if len(pending) == 0 {
		delete(c.captures, k.output)
		return
	}
	c.captures[k.output] = pending
}

func (c *Compositor) runCapture(k *capture) error {
	at := backend.Now()
	var err error
	if k.painted != nil {
		err = k.painted()
	}
	return errors.Join(err, k.target.fill(c, k, func(err error) error { return k.done(at, err) }))
}

func (c *Compositor) outputDraw(o *output.Output, region image.Rectangle, cursor bool) func(render.Pass) error {
	return func(p render.Pass) error {
		last := c.sceneOf(o).prev
		to := c.captureOut(o)
		(&frame{ops: last.ops[:last.cursorFrom]}).replayFrom(p, region.Min, to)
		if !cursor {
			return nil
		}
		ops := frame{ops: last.ops[last.cursorFrom:]}
		if len(ops.ops) == 0 {
			if _, err := c.drawCursor(&ops, o, nil); err != nil {
				return err
			}
		}
		ops.replayFrom(p, region.Min, to)
		return nil
	}
}

func (c *Compositor) windowRegion(w *window) image.Rectangle {
	return w.output.Scale().Rect(w.rect.Sub(w.output.Position()))
}

func (c *Compositor) windowDraw(w *window, cursor bool) func(render.Pass) error {
	return func(p render.Pass) error {
		if w.output == nil {
			return nil
		}
		var f frame
		c.drawTree(&f, w.output, w.top.Surface(), c.windowOrigin(w), nil)
		c.drawPopups(&f, w.output, c.popups(w), nil, popupBlur{})
		if cursor {
			if _, err := c.drawCursor(&f, w.output, nil); err != nil {
				return err
			}
		}
		f.replayFrom(p, c.windowRegion(w).Min, c.captureOut(w.output))
		return nil
	}
}

func (c *Compositor) cursorBounds(o *output.Output) (image.Rectangle, error) {
	var f frame
	if _, err := c.drawCursor(&f, o, nil); err != nil {
		return image.Rectangle{}, err
	}
	return f.bounds(), nil
}

func (c *Compositor) pointerDraw(o *output.Output) func(render.Pass) error {
	return func(p render.Pass) error {
		var f frame
		if _, err := c.drawCursor(&f, o, nil); err != nil {
			return err
		}
		f.replayFrom(p, f.bounds().Min, c.captureOut(o))
		return nil
	}
}

func (c *Compositor) inWindow(w *window, s *surface.Surface) bool {
	if inTree(w.top.Surface(), s) {
		return true
	}
	for _, pp := range c.popups(w) {
		if inTree(pp.popup.Surface(), s) {
			return true
		}
	}
	return false
}

func (c *Compositor) captureRegion(o *output.Output, logical image.Rectangle) image.Rectangle {
	bounds := image.Rectangle{Max: o.Backend().State().Mode.Size}
	if logical == (image.Rectangle{}) {
		return bounds
	}
	return o.Scale().Rect(logical).Intersect(bounds)
}

var (
	errConstraints = errors.New("the buffer breaks the capture's constraints")
	errUndrawable  = fmt.Errorf("%w: the renderer cannot draw into this layout", errConstraints)
)

func shmTarget(b *shm.Buffer, size image.Point, formats []render.Format, scratch *captureScratch) (captureTarget, error) {
	switch {
	case b.Size() != size:
		return nil, fmt.Errorf("%w: the buffer is %v, the capture is %v", errConstraints, b.Size(), size)
	case !slices.Contains(formats, b.Format()):
		return nil, fmt.Errorf("%w: format %d cannot hold a capture", errConstraints, b.Format())
	}
	return shmCapture{b: b, scratch: scratch}, nil
}

func (c *Compositor) dmabufTarget(b *dmabuf.Buffer, size image.Point) (captureTarget, error) {
	r, ok := c.renderer.(captureRenderer)
	d := b.DMABUF()
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: this renderer cannot draw into dmabufs", errConstraints)
	case d.Size != size:
		return nil, fmt.Errorf("%w: the buffer is %v, the capture is %v", errConstraints, d.Size, size)
	case !slices.Contains(dmabufCaptureFormats, d.Format):
		return nil, fmt.Errorf("%w: format %v cannot hold a capture", errConstraints, d.Format)
	}
	mods, err := r.RenderableModifiers(d.Format)
	if err != nil {
		return nil, fmt.Errorf("modifiers the renderer draws %v into: %w", d.Format, err)
	}
	if !slices.Contains(mods, d.Modifier) {
		return nil, fmt.Errorf("%w: format %v with modifier %v", errUndrawable, d.Format, d.Modifier)
	}
	return dmabufCapture{b, r}, nil
}
