package compositor

import (
	"errors"
	"fmt"
	"image"
	"os"
	"runtime/pprof"
	"slices"
	"sync"

	"github.com/stubbedev/lys-lib/atomicfile"

	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const debugFade = 8

var (
	damageTint = render.Color{R: 0xff, G: 0x20, B: 0x40, A: 0xff}.Premultiply()
	opaqueTint = render.Color{G: 0xc0, B: 0x60, A: 0x50}.Premultiply()
	planesTint = render.Color{R: 0x60, G: 0x30, B: 0xff, A: 0x40}.Premultiply()
)

const damageAlpha = 0.45

type debugOverlays struct {
	damage, opaque, planes bool
}

type debugDamage struct {
	ages [debugFade][]image.Rectangle
	head int
}

func (d *debugDamage) push(rects []image.Rectangle) {
	d.head = (d.head + debugFade - 1) % debugFade
	d.ages[d.head] = append(d.ages[d.head][:0], rects...)
}

func (d *debugDamage) age(i int) []image.Rectangle {
	return d.ages[(d.head+i)%debugFade]
}

func (d *debugDamage) lit() bool {
	return slices.ContainsFunc(d.ages[:], func(r []image.Rectangle) bool { return len(r) > 0 })
}

func (d *debugDamage) clear() {
	for i := range d.ages {
		d.ages[i] = d.ages[i][:0]
	}
}

func (d *debugOverlays) fading(sc *scene) bool {
	return d != nil && d.damage && sc.debug.lit()
}

func (d *debugOverlays) tintsComposited() bool {
	return d != nil && d.planes
}

func (d *debugOverlays) draw(sc *scene) {
	cur := &sc.cur
	from := len(cur.ops)
	if d.damage {
		sc.debug.push(sc.damage.Rects())
		for i := range debugFade {
			tint := scaleColor(damageTint, damageAlpha*float32(debugFade-i)/debugFade)
			for _, r := range sc.debug.age(i) {
				cur.FillRect(r, tint)
			}
		}
	}
	if d.opaque {
		for i := range from {
			if op := cur.ops[i]; op.surface != nil {
				op.solid(func(r image.Rectangle) { cur.FillRect(r, opaqueTint) })
			}
		}
	}
	cur.debugOps = len(cur.ops) - from
	shown, drawn := sc.prev.debugTail(), cur.debugTail()
	if slices.Equal(shown, drawn) {
		return
	}
	for _, op := range shown {
		sc.damage.Add(op.dst)
	}
	for _, op := range drawn {
		sc.damage.Add(op.dst)
	}
}

func (f *frame) sceneOps() []drawOp {
	return f.ops[:len(f.ops)-f.debugOps]
}

func (f *frame) debugTail() []drawOp {
	return f.ops[len(f.ops)-f.debugOps:]
}

func (c *Compositor) debugOverlay(a wm.DebugOverlayAction) error {
	next := debugOverlays{}
	if c.debugDraw != nil {
		next = *c.debugDraw
	}
	switch a.Overlay {
	case wm.DebugDamage:
		next.damage = a.Mode.From(next.damage)
	case wm.DebugOpaque:
		next.opaque = a.Mode.From(next.opaque)
	case wm.DebugPlanes:
		next.planes = a.Mode.From(next.planes)
	default:
		return fmt.Errorf("compositor: unknown debug overlay %s", a.Overlay)
	}
	c.debugDraw = &next
	if next == (debugOverlays{}) {
		c.debugDraw = nil
	}
	for _, sc := range c.scenes {
		if !next.damage {
			sc.debug.clear()
		}
		sc.invalidate()
	}
	return c.scheduleAll()
}

type debugRecorders struct {
	trace   *frametrace.Recorder
	profile *os.File
	dumps   sync.WaitGroup
}

func (c *Compositor) debugRecord(a wm.DebugRecordAction) error {
	switch a.Recording {
	case wm.DebugTrace:
		if a.Stop {
			return c.stopDebugTrace()
		}
		return c.startDebugTrace(a.Path)
	case wm.DebugProfile:
		if a.Stop {
			return c.stopDebugProfile()
		}
		return c.startDebugProfile(a.Path)
	case wm.DebugHeap:
		go func() {
			if err := writeHeapProfile(a.Path); err != nil {
				c.loop.Hand("a failed heap profile", func() error {
					c.report(err)
					return nil
				})
			}
		}()
		return nil
	}
	return fmt.Errorf("compositor: unknown debug recording %s", a.Recording)
}

func (c *Compositor) startDebugTrace(path string) error {
	if c.frames != nil {
		return errors.New("compositor: a frame trace is already recording")
	}
	if c.hooks.OpenFrameTrace == nil {
		return errors.New("compositor: this lys-wm cannot record a frame trace")
	}
	r, err := c.hooks.OpenFrameTrace(path)
	if err != nil {
		return fmt.Errorf("compositor: start the frame trace %s: %w", path, err)
	}
	c.recorders.trace = r
	c.tracedCounts, c.traced = c.renderer.Counts(), c.tallied
	c.traced.copied = c.copiedBytes.Load()
	c.frames = r
	return nil
}

func (c *Compositor) stopDebugTrace() error {
	r := c.recorders.trace
	if r == nil {
		return errors.New("compositor: no frame trace was started with debug trace start")
	}
	c.recorders.trace, c.frames = nil, nil
	if err := r.Close(); err != nil {
		return fmt.Errorf("compositor: stop the frame trace: %w", err)
	}
	return nil
}

func (c *Compositor) startDebugProfile(path string) error {
	if c.recorders.profile != nil {
		return errors.New("compositor: a CPU profile is already recording")
	}
	f, err := os.Create(path) //nolint:gosec // the path comes from the user's own debug action
	if err != nil {
		return fmt.Errorf("compositor: create the CPU profile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		return errors.Join(fmt.Errorf("compositor: start the CPU profile %s: %w", path, err), f.Close())
	}
	c.recorders.profile = f
	return nil
}

func (c *Compositor) stopDebugProfile() error {
	f := c.recorders.profile
	if f == nil {
		return errors.New("compositor: no CPU profile was started with debug profile start")
	}
	c.recorders.profile = nil
	pprof.StopCPUProfile()
	if err := f.Close(); err != nil {
		return fmt.Errorf("compositor: write the CPU profile: %w", err)
	}
	return nil
}

func (c *Compositor) stopDebugRecorders() error {
	c.recorders.dumps.Wait()
	var errs []error
	if c.recorders.trace != nil {
		errs = append(errs, c.stopDebugTrace())
	}
	if c.recorders.profile != nil {
		errs = append(errs, c.stopDebugProfile())
	}
	return errors.Join(errs...)
}

func (c *Compositor) dumpProtocol(path string) error {
	dump, err := c.display.AppendProtocol(nil)
	if err != nil {
		return fmt.Errorf("compositor: dump the protocol to %s: %w", path, err)
	}
	c.recorders.dumps.Go(func() {
		if err := atomicfile.WriteFile(path, dump, 0o600); err != nil {
			c.loop.Hand("a failed protocol dump", func() error {
				c.report(fmt.Errorf("compositor: dump the protocol: %w", err))
				return nil
			})
		}
	})
	return nil
}

func writeHeapProfile(path string) error {
	f, err := os.Create(path) //nolint:gosec // the path comes from the user's own debug action
	if err != nil {
		return fmt.Errorf("compositor: create the heap profile: %w", err)
	}
	if err := pprof.WriteHeapProfile(f); err != nil {
		return errors.Join(fmt.Errorf("compositor: write the heap profile %s: %w", path, err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("compositor: write the heap profile %s: %w", path, err)
	}
	return nil
}

func (c *Compositor) tintComposited(p render.Pass, bounds image.Rectangle) {
	if c.debugDraw.tintsComposited() {
		p.FillRect(bounds, planesTint, render.Mask{})
	}
}
