// Package syncobj implements wp_linux_drm_syncobj_manager_v1: explicit
// synchronization through DRM timeline syncobjs. A commit waits for its
// acquire point before it takes effect, and a buffer's release point signals
// once the GPU has finished with it and it has left the screen.
package syncobj

import (
	"errors"
	"fmt"
	"slices"

	"github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/loop"
	sls "github.com/stubbedev/lys-wm/internal/protocol/server/linuxdrmsyncobj"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const version = 1

type Fencer interface {
	SyncFile() (int, error)
}

type Screen interface {
	OffScreen(b *dmabuf.Buffer, f func() error) error
	Awaiting() error
}

type Device interface {
	Path() string
	Cap(capability uint64) (uint64, error)
	ImportSyncobj(fd int) (drm.Syncobj, error)
	DestroySyncobj(s drm.Syncobj) error
	Signal(s drm.Syncobj, point uint64) error
	Query(s drm.Syncobj) (uint64, error)
	EventFD(s drm.Syncobj, point uint64, eventfd int, available bool) error
	ImportSyncFile(s drm.Syncobj, point uint64, syncFile int) error
}

type Manager struct {
	loop     *loop.Loop
	dev      Device
	fencer   Fencer
	screen   Screen
	surfaces map[*surface.Surface]*syncSurface
	owed     []Release
	eventFDs uint64
	sweeps   uint64
}

func (m *Manager) Sweep(acquireWaiting func(s *surface.Surface, acquire, sweeps uint64), releaseOwed func(r Release, sweeps uint64)) (bool, error) {
	m.sweeps++
	busy := false
	for s, ss := range m.surfaces {
		if len(ss.waits) == 0 {
			continue
		}
		busy = true
		w := ss.waits[0]
		acquireWaiting(s, w.acquire.value, m.sweeps-w.born)
	}
	var errs []error
	owed := m.owed[:0]
	for _, r := range m.owed {
		reached, err := r.reached()
		if err != nil || reached {
			errs = append(errs, err, r.p.drop())
			continue
		}
		owed = append(owed, r)
		releaseOwed(r, m.sweeps-r.born)
	}
	clear(m.owed[len(owed):])
	m.owed = owed
	return busy || len(owed) > 0, errors.Join(errs...)
}

func New(d *server.Display, l *loop.Loop, dev Device, f Fencer, screen Screen) (*Manager, error) {
	timelines, err := dev.Cap(drm.CapSyncobjTimeline)
	if err != nil {
		return nil, fmt.Errorf("syncobj: %w", err)
	}
	if timelines == 0 {
		return nil, fmt.Errorf("syncobj: %s has no timeline syncobjs", dev.Path())
	}
	m := &Manager{loop: l, dev: dev, fencer: f, screen: screen, surfaces: map[*surface.Surface]*syncSurface{}}
	if _, err := d.AddGlobal(sls.WpLinuxDrmSyncobjManagerV1Interface, version, m.bind); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) bind(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := sls.NewWpLinuxDrmSyncobjManagerV1(c, id, v, requests{m, c})
	return err
}

type timeline struct {
	m      *Manager
	handle drm.Syncobj
	refs   int
}

func (t *timeline) ref() *timeline {
	t.refs++
	return t
}

func (t *timeline) unref() error {
	if t.refs--; t.refs > 0 {
		return nil
	}
	return t.m.dev.DestroySyncobj(t.handle)
}

type point struct {
	t     *timeline
	value uint64
	res   *sls.WpLinuxDrmSyncobjTimelineV1
}

func newPoint(res *sls.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) (*point, error) {
	h, ok := res.Handler().(*timelineRequests)
	if !ok {
		return nil, fmt.Errorf("syncobj: timeline@%d has handler %T", res.Base().ID(), res.Handler())
	}
	return &point{t: h.t.ref(), value: wire.Join(hi, lo), res: res}, nil
}

func (p *point) drop() error {
	if p == nil {
		return nil
	}
	return p.t.unref()
}

func (p *point) armNow() (arming, error) {
	return p.armInstead(nil)
}

func (p *point) armInstead(cause error) (arming, error) {
	if err := p.t.m.dev.Signal(p.t.handle, p.value); err != nil {
		return unarmed, errors.Join(cause, fmt.Errorf("syncobj: signal release point %d: %w", p.value, err))
	}
	return signalled, cause
}

func (p *point) armAfterGPU() (arming, error) {
	fd, err := p.t.m.fencer.SyncFile()
	if err != nil {
		return p.armInstead(fmt.Errorf("syncobj: fence release point %d, signalled at once instead: %w", p.value, err))
	}
	if fd < 0 {
		return p.armNow()
	}
	err = p.t.m.dev.ImportSyncFile(p.t.handle, p.value, fd)
	closed := unix.Close(fd)
	if err != nil {
		how, signalErr := p.armInstead(fmt.Errorf("syncobj: import the GPU fence into release point %d, signalled at once instead: %w", p.value, err))
		return how, errors.Join(signalErr, closed)
	}
	return fenced, closed
}

type arming uint8

const (
	fenced arming = iota + 1
	signalled
	unarmed
)

var armingNames = [...]string{
	fenced:    "the GPU fence lys-wm attached to it when it let the buffer go",
	signalled: "nothing lys-wm holds: it signalled the point when it let the buffer go, yet the kernel's timeline has not reached it",
	unarmed:   "nothing: lys-wm failed to fence or signal it, so nothing ever will",
}

type Release struct {
	p       *point
	surface *surface.Surface
	buffer  *dmabuf.Buffer
	arming  arming
	born    uint64
}

func (r Release) Surface() *surface.Surface {
	return r.surface
}

func (r Release) Buffer() *dmabuf.Buffer {
	return r.buffer
}

func (r Release) Awaits() string {
	return armingNames[r.arming]
}

func (r Release) String() string {
	return fmt.Sprintf("release point %d of timeline@%d (syncobj handle %d)", r.p.value, r.p.res.Base().ID(), r.p.t.handle)
}

func (r Release) reached() (bool, error) {
	at, err := r.p.t.m.dev.Query(r.p.t.handle)
	if err != nil {
		return false, fmt.Errorf("syncobj: query %s of %s: %w", r, r.buffer, err)
	}
	return at >= r.p.value, nil
}

func (m *Manager) owe(s *surface.Surface, l latch, how arming, err error) error {
	m.owed = append(m.owed, Release{p: l.release, surface: s, buffer: l.buffer, arming: how, born: m.sweeps})
	return errors.Join(err, m.screen.Awaiting())
}

type requests struct {
	m *Manager
	c *server.Client
}

func (requests) Destroy(*sls.WpLinuxDrmSyncobjManagerV1) error {
	return nil
}

func (r requests) ImportTimeline(obj *sls.WpLinuxDrmSyncobjManagerV1, res *sls.WpLinuxDrmSyncobjTimelineV1, fd int) (sls.WpLinuxDrmSyncobjTimelineV1Handler, error) {
	handle, err := r.m.dev.ImportSyncobj(fd)
	if closeErr := unix.Close(fd); closeErr != nil && err == nil {
		return nil, errors.Join(closeErr, r.m.dev.DestroySyncobj(handle))
	}
	if err != nil {
		return nil, obj.ProtocolError(sls.WpLinuxDrmSyncobjManagerV1ErrorInvalidTimeline, "not a syncobj: %v", err)
	}
	t := &timeline{m: r.m, handle: handle, refs: 1}
	server.OnDestroy(res, t.unref)
	return &timelineRequests{t: t}, nil
}

type timelineRequests struct{ t *timeline }

func (*timelineRequests) Destroy(*sls.WpLinuxDrmSyncobjTimelineV1) error {
	return nil
}

func (r requests) GetSurface(obj *sls.WpLinuxDrmSyncobjManagerV1, res *sls.WpLinuxDrmSyncobjSurfaceV1, wl *sw.WlSurface) (sls.WpLinuxDrmSyncobjSurfaceV1Handler, error) {
	s, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if prev, ok := r.m.surfaces[s]; ok && prev.res != nil {
		return nil, obj.ProtocolError(sls.WpLinuxDrmSyncobjManagerV1ErrorSurfaceExists, "wl_surface@%d already has a syncobj surface", wl.Base().ID())
	}
	ss, ok := r.m.surfaces[s]
	if !ok {
		ss = &syncSurface{m: r.m, s: s}
		ss.hooks = &surface.Hooks{Commit: ss.commit, Destroyed: ss.surfaceGone, Handover: ss.handover}
		s.AddHooks(ss.hooks)
		r.m.surfaces[s] = ss
	}
	ss.res = res
	server.OnDestroy(res, ss.objectGone)
	return ss, nil
}

type wait struct {
	src     *loop.Source
	efd     int
	acquire *point
	hold    *surface.Hold
	born    uint64
}

func (w *wait) cancel() error {
	return errors.Join(w.src.Remove(), unix.Close(w.efd), w.acquire.drop())
}

func (w *wait) close() error {
	return errors.Join(w.src.Remove(), unix.Close(w.efd))
}

type syncSurface struct {
	m       *Manager
	s       *surface.Surface
	res     *sls.WpLinuxDrmSyncobjSurfaceV1
	hooks   *surface.Hooks
	acquire *point
	release *point
	latched []latch
	current latch
	waits   []*wait
	idle    []*wait
}

type latch struct {
	release *point
	buffer  *dmabuf.Buffer
	acquire *sls.WpLinuxDrmSyncobjTimelineV1
	value   uint64
}

func (ss *syncSurface) handover(st *server.Stream) error {
	if ss.res == nil || ss.current.buffer == nil {
		return nil
	}
	l := ss.current
	if l.acquire == nil || l.acquire.Base().Removed() || l.release == nil || l.release.res.Base().Removed() {
		return fmt.Errorf("syncobj: wl_surface@%d is on screen under a timeline its client destroyed", ss.s.Resource().Base().ID())
	}
	point := func(name string, res *sls.WpLinuxDrmSyncobjTimelineV1, value uint64) error {
		e, err := st.Request(ss.res.Base().ID(), sls.WpLinuxDrmSyncobjSurfaceV1Interface, name)
		if err != nil {
			return err
		}
		hi, lo := wire.Halves(value)
		e.Object(res.Base().ID())
		e.Uint(hi)
		e.Uint(lo)
		return e.End()
	}
	return errors.Join(point("set_acquire_point", l.acquire, l.value), point("set_release_point", l.release.res, l.release.value))
}

func (ss *syncSurface) retire(l latch) error {
	if l.release == nil {
		return nil
	}
	return ss.m.screen.OffScreen(l.buffer, func() error { return ss.letGo(l, (*point).armAfterGPU) })
}

func (ss *syncSurface) letGo(l latch, arm func(*point) (arming, error)) error {
	if l.release == nil {
		return nil
	}
	how, err := arm(l.release)
	return ss.m.owe(ss.s, l, how, err)
}

func (ss *syncSurface) Destroy(*sls.WpLinuxDrmSyncobjSurfaceV1) error {
	return nil
}

func (ss *syncSurface) SetAcquirePoint(obj *sls.WpLinuxDrmSyncobjSurfaceV1, t *sls.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) error {
	p, err := newPoint(t, hi, lo)
	if err != nil {
		return err
	}
	err = ss.acquire.drop()
	ss.acquire = p
	return err
}

func (ss *syncSurface) SetReleasePoint(obj *sls.WpLinuxDrmSyncobjSurfaceV1, t *sls.WpLinuxDrmSyncobjTimelineV1, hi, lo uint32) error {
	p, err := newPoint(t, hi, lo)
	if err != nil {
		return err
	}
	err = ss.release.drop()
	ss.release = p
	return err
}

func (ss *syncSurface) fail(code sls.WpLinuxDrmSyncobjSurfaceV1Error, format string, args ...any) error {
	return ss.res.ProtocolError(code, format, args...)
}

func (ss *syncSurface) check(buffer surface.Buffer, attached bool) (*dmabuf.Buffer, error) {
	acquire, release := ss.acquire, ss.release
	switch {
	case (acquire != nil || release != nil) && (!attached || buffer == nil):
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorNoBuffer, "acquire or release point without a buffer")
	case !attached || buffer == nil:
		return nil, nil
	case acquire == nil:
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorNoAcquirePoint, "buffer without an acquire point")
	case release == nil:
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorNoReleasePoint, "buffer without a release point")
	case acquire.t == release.t && acquire.value >= release.value:
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorConflictingPoints, "acquire point %d is not before release point %d on the same timeline", acquire.value, release.value)
	}
	b, ok := buffer.(*dmabuf.Buffer)
	if !ok {
		return nil, ss.fail(sls.WpLinuxDrmSyncobjSurfaceV1ErrorUnsupportedBuffer, "explicit sync needs a dmabuf, not %T", buffer)
	}
	return b, nil
}

func (ss *syncSurface) commit(u *surface.Update) error {
	buffer, attached := ss.s.PendingBuffer()
	var explicit *dmabuf.Buffer
	if ss.res != nil {
		var err error
		if explicit, err = ss.check(buffer, attached); err != nil {
			return err
		}
	}
	if !attached {
		return nil
	}
	acquire, release := ss.acquire, ss.release
	ss.acquire, ss.release = nil, nil
	l := latch{release: release, buffer: explicit}
	if acquire != nil {
		l.acquire, l.value = acquire.res, acquire.value
	}
	ss.latched = append(ss.latched, l)
	u.OnApply(ss.applied)
	if acquire == nil {
		return nil
	}
	u.ExplicitlySynced()
	return ss.wait(acquire, u.Hold())
}

func (ss *syncSurface) wait(acquire *point, hold *surface.Hold) error {
	w, err := ss.idleWait()
	if err != nil {
		return errors.Join(err, acquire.drop())
	}
	if err := ss.m.dev.EventFD(acquire.t.handle, acquire.value, w.efd, false); err != nil {
		return errors.Join(err, w.close(), acquire.drop())
	}
	w.acquire, w.hold, w.born = acquire, hold, ss.m.sweeps
	ss.waits = append(ss.waits, w)
	return ss.m.screen.Awaiting()
}

func (ss *syncSurface) idleWait() (*wait, error) {
	if n := len(ss.idle); n > 0 {
		w := ss.idle[n-1]
		ss.idle = ss.idle[:n-1]
		return w, nil
	}
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("syncobj: eventfd: %w", err)
	}
	ss.m.eventFDs++
	w := &wait{efd: efd}
	if w.src, err = ss.m.loop.AddFD(efd, loop.Readable|loop.Edge, func(loop.Events) error { return ss.signalled(w) }); err != nil {
		return nil, errors.Join(err, unix.Close(efd))
	}
	return w, nil
}

func (ss *syncSurface) signalled(w *wait) error {
	ss.waits = slices.DeleteFunc(ss.waits, func(x *wait) bool { return x == w })
	acquire, hold := w.acquire, w.hold
	w.acquire, w.hold = nil, nil
	ss.idle = append(ss.idle, w)
	return errors.Join(acquire.drop(), hold.Release())
}

func (ss *syncSurface) closeIdle() error {
	var errs []error
	for _, w := range ss.idle {
		errs = append(errs, w.close())
	}
	ss.idle = nil
	return errors.Join(errs...)
}

func (ss *syncSurface) applied() error {
	err := ss.retire(ss.current)
	ss.current = ss.latched[0]
	ss.latched[0] = latch{}
	ss.latched = ss.latched[1:]
	if ss.res == nil && ss.current.release == nil && len(ss.latched) == 0 {
		err = errors.Join(err, ss.forget())
	}
	return err
}

func (ss *syncSurface) objectGone() error {
	ss.res = nil
	err := errors.Join(ss.acquire.drop(), ss.release.drop())
	ss.acquire, ss.release = nil, nil
	if ss.current.release == nil && len(ss.latched) == 0 {
		err = errors.Join(err, ss.forget())
	}
	return err
}

func (ss *syncSurface) surfaceGone() error {
	errs := []error{ss.retire(ss.current)}
	ss.current = latch{}
	for _, l := range ss.latched {
		errs = append(errs, ss.letGo(l, (*point).armNow))
	}
	ss.latched = nil
	for _, w := range ss.waits {
		errs = append(errs, w.cancel())
	}
	ss.waits = nil
	return errors.Join(append(errs, ss.objectGone())...)
}

func (ss *syncSurface) forget() error {
	ss.s.RemoveHooks(ss.hooks)
	if ss.m.surfaces[ss.s] == ss {
		delete(ss.m.surfaces, ss.s)
	}
	return ss.closeIdle()
}
