// Package shm implements wl_shm: client memory pools and the buffers carved
// out of them, read safely even when a client truncates its pool.
package shm

import (
	"errors"
	"fmt"
	"image"
	"os"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const shmVersion = 2

var wireFormats = map[render.Format]sw.WlShmFormat{
	render.ARGB8888:    sw.WlShmFormatArgb8888,
	render.XRGB8888:    sw.WlShmFormatXrgb8888,
	render.ABGR8888:    sw.WlShmFormatAbgr8888,
	render.XBGR8888:    sw.WlShmFormatXbgr8888,
	render.ARGB2101010: sw.WlShmFormatArgb2101010,
	render.XRGB2101010: sw.WlShmFormatXrgb2101010,
	render.ABGR2101010: sw.WlShmFormatAbgr2101010,
	render.XBGR2101010: sw.WlShmFormatXbgr2101010,
}

type Manager struct {
	formats     map[sw.WlShmFormat]render.Format
	order       []sw.WlShmFormat
	loop        *loop.Loop
	localMemory func(fd int) bool
	loopTouches uint64
	stagingMu   sync.Mutex
	stagingFree [][]byte
}

const (
	stagedPixels    = 1 << 20
	maxFreeStagings = 2
)

func isLocalMemory(fd int) bool {
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return false
	}
	switch uint32(st.Type) { //nolint:gosec // filesystem magics fit 32 bits
	case unix.TMPFS_MAGIC, unix.HUGETLBFS_MAGIC:
		return true
	}
	return false
}

func (m *Manager) LoopTouches() uint64 {
	return m.loopTouches
}

func (m *Manager) takeStaging(n int) []byte {
	m.stagingMu.Lock()
	for i, free := range m.stagingFree {
		if cap(free) >= n {
			m.stagingFree = slices.Delete(m.stagingFree, i, i+1)
			m.stagingMu.Unlock()
			return free[:n]
		}
	}
	m.stagingMu.Unlock()
	return make([]byte, n)
}

func (m *Manager) recycleStaging(pix []byte) {
	m.stagingMu.Lock()
	defer m.stagingMu.Unlock()
	if len(m.stagingFree) < maxFreeStagings {
		m.stagingFree = append(m.stagingFree, pix)
	}
}

func New(d *server.Display, l *loop.Loop, formats []render.Format) (*Manager, error) {
	m := &Manager{formats: map[sw.WlShmFormat]render.Format{}, loop: l, localMemory: isLocalMemory}
	for _, f := range formats {
		wf, ok := wireFormats[f]
		if !ok {
			return nil, fmt.Errorf("shm: renderer format %d has no wl_shm code", f)
		}
		m.formats[wf] = f
		m.order = append(m.order, wf)
	}
	if _, ok := m.formats[sw.WlShmFormatArgb8888]; !ok {
		return nil, errors.New("shm: the renderer cannot sample ARGB8888, which wl_shm requires")
	}
	if _, ok := m.formats[sw.WlShmFormatXrgb8888]; !ok {
		return nil, errors.New("shm: the renderer cannot sample XRGB8888, which wl_shm requires")
	}
	if _, err := d.AddGlobal(sw.WlShmInterface, shmVersion, m.bind); err != nil {
		return nil, err
	}
	d.OnPrepareHandover(m.emit)
	return m, nil
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	shm, err := sw.NewWlShm(c, id, version, shmRequests{m, c})
	if err != nil {
		return err
	}
	for _, f := range m.order {
		if err := shm.SendFormat(f); err != nil {
			return err
		}
	}
	return nil
}

func shmError(object wire.Resource, code sw.WlShmError, format string, args ...any) error {
	return wire.NewProtocolError(object.Base().ID(), uint32(code), format, args...)
}

type shmRequests struct {
	m *Manager
	c *server.Client
}

func (r shmRequests) CreatePool(obj *sw.WlShm, res *sw.WlShmPool, fd int, size int32) (sw.WlShmPoolHandler, error) {
	if size <= 0 {
		_ = unix.Close(fd)
		return nil, obj.ProtocolError(sw.WlShmErrorInvalidStride, "invalid pool size %d", size)
	}
	mapping, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return nil, obj.ProtocolError(sw.WlShmErrorInvalidFD, "cannot map pool fd: %v", err)
	}
	p := &pool{m: r.m, c: r.c, resource: res, fd: fd, mapping: mapping, refs: 1, local: r.m.localMemory(fd)}
	server.OnDestroy(res, p.unref)
	return p, nil
}

func (r shmRequests) Release(*sw.WlShm) error {
	return nil
}

type pool struct {
	m        *Manager
	c        *server.Client
	resource *sw.WlShmPool
	fd       int
	mapping  []byte
	writable []byte
	refs     int
	local    bool
	copying  int
	retired  [][]byte
}

func (p *pool) unref() error {
	p.refs--
	if p.refs > 0 {
		return nil
	}
	errs := []error{unix.Munmap(p.mapping), p.dropWritable(), unix.Close(p.fd)}
	p.mapping, p.fd = nil, -1
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("shm: release pool: %w", err)
	}
	return nil
}

func (p *pool) CreateBuffer(obj *sw.WlShmPool, res *sw.WlBuffer, offset, width, height, stride int32, format sw.WlShmFormat) (sw.WlBufferHandler, error) {
	f, ok := p.m.formats[format]
	if !ok {
		return nil, shmError(obj, sw.WlShmErrorInvalidFormat, "unsupported format %#x", uint32(format))
	}
	size := image.Pt(int(width), int(height))
	if offset < 0 || int(offset)+int(stride)*int(height) > len(p.mapping) {
		return nil, shmError(obj, sw.WlShmErrorInvalidStride, "invalid buffer: offset %d, %dx%d, stride %d in a %d-byte pool", offset, width, height, stride, len(p.mapping))
	}
	if err := render.CheckPixels(size, int(stride), len(p.mapping)-int(offset)); err != nil {
		return nil, shmError(obj, sw.WlShmErrorInvalidStride, "invalid buffer: %v", err)
	}
	b := &Buffer{pool: p, resource: res, offset: int(offset), size: size, stride: int(stride), format: f}
	p.refs++
	b.Backing = surface.NewBacking(res, func() error { return errors.Join(b.dropWriter(), p.unref()) })
	return b, nil
}

func (p *pool) Destroy(*sw.WlShmPool) error {
	return nil
}

func (p *pool) Resize(obj *sw.WlShmPool, size int32) error {
	switch {
	case int(size) < len(p.mapping):
		return shmError(obj, sw.WlShmErrorInvalidFD, "shrinking the pool from %d to %d bytes", len(p.mapping), size)
	case int(size) == len(p.mapping):
		return nil
	}
	mapping, err := unix.Mmap(p.fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return shmError(obj, sw.WlShmErrorInvalidFD, "cannot map the pool at %d bytes: %v", size, err)
	}
	if p.copying > 0 {
		p.retired = append(p.retired, p.mapping)
		p.mapping = mapping
		return p.dropWritable()
	}
	if err := errors.Join(unix.Munmap(p.mapping), p.dropWritable()); err != nil {
		return fmt.Errorf("shm: unmap the old pool mapping: %w", err)
	}
	p.mapping = mapping
	return nil
}

func (p *pool) dropWritable() error {
	if p.writable == nil {
		return nil
	}
	err := unix.Munmap(p.writable)
	p.writable = nil
	return err
}

func (p *pool) writableMapping() ([]byte, error) {
	if p.writable != nil {
		return p.writable, nil
	}
	m, err := unix.Mmap(p.fd, 0, len(p.mapping), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("shm: map the pool for writing: %w", err)
	}
	p.writable = m
	return m, nil
}

type Buffer struct {
	*surface.Backing
	pool     *pool
	resource *sw.WlBuffer
	offset   int
	size     image.Point
	stride   int
	format   render.Format
	writer   *Mapping
	stages   []*staged
}

type staged struct {
	pix  []byte
	err  error
	done bool
}

func (b *Buffer) Size() image.Point {
	return b.size
}

func (b *Buffer) Format() render.Format {
	return b.format
}

func (b *Buffer) Destroy(*sw.WlBuffer) error {
	return nil
}

func (b *Buffer) Release() error {
	return b.resource.SendRelease()
}

func (b *Buffer) Write(f func(data []byte, stride int) error) (err error) {
	m, err := b.pool.writableMapping()
	if err != nil {
		return err
	}
	return b.guard(b.within(m, 0), f)
}

func (b *Buffer) Read(f func(data []byte, stride int) error) (err error) {
	if s := b.takeStaged(); s != nil {
		if s.err != nil {
			return s.err
		}
		defer b.pool.m.recycleStaging(s.pix)
		return f(s.pix, b.stride)
	}
	if !b.pool.local {
		b.pool.m.loopTouches++
	}
	return b.guard(b.within(b.pool.mapping, 0), f)
}

func (b *Buffer) LocalMemory() bool {
	return b.pool.local
}

func (b *Buffer) takeStaged() *staged {
	var newest *staged
	kept := b.stages[:0]
	for _, s := range b.stages {
		switch {
		case !s.done:
			kept = append(kept, s)
		case newest == nil:
			newest = s
		default:
			b.pool.m.recycleStaging(newest.pix)
			newest = s
		}
	}
	clear(b.stages[len(kept):])
	b.stages = kept
	return newest
}

func (b *Buffer) AwaitWriter(c surface.Awaiter) error {
	p := b.pool
	if p.local && c.DamagedPixels() < stagedPixels {
		return nil
	}
	release := c.Await()
	s := &staged{}
	b.stages = append(b.stages, s)
	p.refs++
	p.copying++
	from := b.within(p.mapping, 0)
	go func() {
		s.pix = p.m.takeStaging(len(from))
		s.err = b.guard(from, func(data []byte, _ int) error {
			copy(s.pix, data)
			return nil
		})
		err := p.m.loop.Post(func() error {
			s.done = true
			return errors.Join(p.copied(), release())
		})
		if err != nil && !errors.Is(err, loop.ErrClosed) {
			panic(fmt.Sprintf("shm: hand a staged buffer back to the loop: %v", err))
		}
	}()
	return nil
}

func (p *pool) copied() error {
	p.copying--
	var errs []error
	if p.copying == 0 {
		for _, m := range p.retired {
			errs = append(errs, unix.Munmap(m))
		}
		p.retired = nil
	}
	return errors.Join(append(errs, p.unref())...)
}

func (b *Buffer) within(mapping []byte, at int) []byte {
	return mapping[b.offset-at : b.offset-at+b.stride*b.size.Y]
}

type Mapping struct {
	b     *Buffer
	pages []byte
	at    int
	refs  atomic.Int32
}

func (b *Buffer) MapForWriting() (*Mapping, error) {
	if b.writer == nil {
		at := b.offset &^ (os.Getpagesize() - 1)
		pages, err := unix.Mmap(b.pool.fd, int64(at), b.offset+b.stride*b.size.Y-at, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return nil, fmt.Errorf("shm: map a %v buffer at offset %d for writing: %w", b.size, b.offset, err)
		}
		b.writer = &Mapping{b: b, pages: pages, at: at}
		b.writer.refs.Store(1)
	}
	b.writer.refs.Add(1)
	return b.writer, nil
}

func (b *Buffer) dropWriter() error {
	if b.writer == nil {
		return nil
	}
	m := b.writer
	b.writer = nil
	return m.Close()
}

func (m *Mapping) Write(f func(data []byte, stride int) error) error {
	return m.b.guard(m.b.within(m.pages, m.at), f)
}

func (m *Mapping) Close() error {
	if m.refs.Add(-1) > 0 {
		return nil
	}
	if err := unix.Munmap(m.pages); err != nil {
		return fmt.Errorf("shm: unmap a %v buffer mapped for writing: %w", m.b.size, err)
	}
	return nil
}

func (b *Buffer) guard(data []byte, f func(data []byte, stride int) error) (err error) {
	old := debug.SetPanicOnFault(true)
	defer debug.SetPanicOnFault(old)
	defer func() {
		if r := recover(); r != nil {
			fault, ok := r.(interface{ Addr() uintptr })
			if !ok {
				panic(r)
			}
			err = shmError(b.resource, sw.WlShmErrorInvalidFD, "error accessing the buffer's pool at %#x: the client shrank its file", fault.Addr())
		}
	}()
	return f(data, b.stride)
}

func (m *Manager) emit(c *server.Client, st *server.Stream) error {
	pools := map[*pool]wire.ObjectID{}
	emitPool := func(p *pool, id wire.ObjectID) (wire.ObjectID, error) {
		if got, ok := pools[p]; ok {
			return got, nil
		}
		shm, err := st.Global(sw.WlShmInterface)
		if err != nil {
			return 0, err
		}
		if id == 0 {
			id = st.Temp()
		}
		e, err := st.Request(shm, sw.WlShmInterface, "create_pool")
		if err != nil {
			return 0, err
		}
		e.Object(id)
		e.FD(p.fd)
		e.Int(int32(len(p.mapping))) //nolint:gosec // a pool's size came from an int32 argument
		if err := e.End(); err != nil {
			return 0, err
		}
		pools[p] = id
		return id, nil
	}
	objects := c.Objects()
	for _, r := range objects {
		if res, ok := r.(*sw.WlShmPool); ok {
			p, ok := res.Handler().(*pool)
			if !ok {
				return fmt.Errorf("shm: wl_shm_pool@%d has handler %T", res.Base().ID(), res.Handler())
			}
			if _, err := emitPool(p, res.Base().ID()); err != nil {
				return err
			}
		}
	}
	for _, r := range objects {
		res, ok := r.(*sw.WlBuffer)
		if !ok {
			continue
		}
		b, ok := res.Handler().(*Buffer)
		if !ok {
			continue
		}
		id, err := emitPool(b.pool, 0)
		if err != nil {
			return err
		}
		wf := sw.WlShmFormatArgb8888
		if w, ok := wireFormats[b.format]; ok {
			wf = w
		}
		e, err := st.Request(id, sw.WlShmPoolInterface, "create_buffer")
		if err != nil {
			return err
		}
		e.Object(res.Base().ID())
		e.Int(int32(b.offset)) //nolint:gosec // these came from int32 arguments
		e.Int(int32(b.size.X)) //nolint:gosec // these came from int32 arguments
		e.Int(int32(b.size.Y)) //nolint:gosec // these came from int32 arguments
		e.Int(int32(b.stride)) //nolint:gosec // these came from int32 arguments
		e.Uint(uint32(wf))
		if err := e.End(); err != nil {
			return err
		}
		st.Emitted(res)
	}
	return nil
}
