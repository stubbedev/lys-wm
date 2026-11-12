package compositor

import (
	"errors"
	"fmt"
	"image"
	"sync/atomic"

	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/shm"
)

func (c *Compositor) offLoop(work func() error, then func(error) error) {
	c.offLoopWork.Go(func() {
		err := work()
		c.loop.Hand("off-loop work", func() error { return then(err) })
	})
}

func (c *Compositor) CapturedPixels() uint64 {
	return c.capturedPixels
}

func (c *Compositor) ShmSyncReads() uint64 {
	return c.shmSyncReads
}

func (c *Compositor) asyncReader() (render.AsyncReader, bool) {
	r, ok := c.renderer.(render.AsyncReader)
	return r, ok && r.AsyncReads()
}

type captureScratch struct {
	target render.AsyncReadTarget
	read   *asyncRead
	pix    []byte
}

func (s *captureScratch) take(r render.AsyncReader, size image.Point) (t render.AsyncReadTarget, kept bool, err error) {
	if s.read != nil {
		s.read.owner, s.read, s.target = nil, nil, nil
	}
	if s.target != nil && s.target.Size() == size {
		return s.target, true, nil
	}
	if err := s.drop(); err != nil {
		return nil, false, err
	}
	t, err = r.NewAsyncTarget(size)
	if err != nil {
		return nil, false, err
	}
	s.target = t
	return t, false, nil
}

func (s *captureScratch) drop() error {
	t := s.target
	s.target, s.pix = nil, nil
	if s.read != nil {
		s.read.owner, s.read = nil, nil
		return nil
	}
	if t == nil {
		return nil
	}
	return t.Destroy()
}

type asyncRead struct {
	c         *Compositor
	k         *capture
	target    render.AsyncReadTarget
	owner     *captureScratch
	pending   render.PendingRead
	dst       frameWriter
	finish    func(error) error
	cancelled atomic.Bool
	ended     bool
}

type frameWriter interface {
	Write(f func(data []byte, stride int) error) error
	Close() error
}

func (r *asyncRead) cancel() {
	r.cancelled.Store(true)
}

func (r *asyncRead) copyRows(dst frameWriter, pix []byte, stride int, update render.Region) error {
	err := dst.Write(func(data []byte, dstStride int) error {
		r.c.copiedBytes.Add(copyRegion(data, dstStride, pix, stride, update, r.cancelled.Load))
		return nil
	})
	return errors.Join(err, dst.Close())
}

func (c *Compositor) readAsync(r render.AsyncReader, b *shm.Buffer, scratch *captureScratch, k *capture, finish func(error) error) error {
	oneShot := scratch == nil
	if oneShot {
		scratch = &captureScratch{}
	}
	target, kept, err := scratch.take(r, k.size)
	if err != nil {
		return finish(err)
	}
	read := &asyncRead{c: c, k: k, target: target, finish: finish}
	if !oneShot {
		read.owner, scratch.read = scratch, read
	}
	if kept && k.partial {
		err = c.paintRegion(target, k, k.repaint)
	} else {
		err = c.paintRegion(target, k, render.Region{image.Rectangle{Max: k.size}})
	}
	if err != nil {
		return read.end(err)
	}
	if read.pending, err = target.ReadAsync(); err != nil {
		return read.end(err)
	}
	dst, err := b.MapForWriting()
	if err != nil {
		return read.end(err)
	}
	read.dst = dst
	k.read = read
	c.reads[read] = struct{}{}
	c.offLoop(read.pending.Wait, read.waited)
	return nil
}

func (r *asyncRead) waited(err error) error {
	if err != nil || r.cancelled.Load() {
		return r.end(err)
	}
	pix, stride, err := r.pending.Map()
	if err != nil {
		return r.end(err)
	}
	update, dst := r.k.updated(), r.dst
	r.dst = nil
	r.c.offLoop(func() error { return r.copyRows(dst, pix, stride, update) }, r.end)
	return nil
}

func (c *Compositor) reportUnmap(err error) error {
	if err != nil {
		c.report(fmt.Errorf("compositor: release a capture's mapping: %w", err))
	}
	return nil
}

var errCaptureAbandoned = fmt.Errorf("compositor: the frame was destroyed while it was read back: %w", errCaptureGone)

func (r *asyncRead) end(err error) error {
	if r.ended {
		return nil
	}
	r.ended = true
	delete(r.c.reads, r)
	if r.pending != nil {
		err = errors.Join(err, r.pending.Release())
	}
	if dst := r.dst; dst != nil {
		r.dst = nil
		r.c.offLoop(dst.Close, r.c.reportUnmap)
	}
	if r.owner == nil {
		err = errors.Join(err, r.target.Destroy())
	} else {
		r.owner.read = nil
	}
	if r.cancelled.Load() {
		if err != nil {
			r.c.report(fmt.Errorf("compositor: an abandoned capture: %w", err))
		}
		err = errCaptureAbandoned
	}
	return r.finish(err)
}

func (c *Compositor) endReads() error {
	c.execs.Wait()
	c.labelWork.Wait()
	c.offLoopWork.Wait()
	var errs []error
	for r := range c.reads {
		r.cancel()
		errs = append(errs, r.end(nil))
	}
	c.offLoopWork.Wait()
	for r := range c.cursorReads {
		errs = append(errs, r.end(false))
	}
	for _, hw := range c.hwCursors {
		errs = append(errs, hw.release())
	}
	return errors.Join(errs...)
}
