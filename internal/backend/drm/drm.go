// Package drm is the native backend: one DRM device driven with atomic KMS,
// scanning out GBM buffers that the GLES or Vulkan renderer draws into.
package drm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	kdrm "github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/edid"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var deepFormats = []fourcc.Format{fourcc.XRGB2101010, fourcc.XBGR2101010}

type layout struct {
	format    fourcc.Format
	modifiers []fourcc.Modifier
	route     route
}

type route uint8

const (
	primaryBuffer route = iota
	ownBuffer
)

const (
	scanoutFormat = fourcc.XRGB8888
	cursorFormat  = fourcc.ARGB8888
	buffersPerOut = 2
	cursorBuffers = 3
	eventBuffer   = 1024
)

type device interface {
	FD() int
	Number() (uint64, error)
	Cap(capability uint64) (uint64, error)
	Resources() (kdrm.Resources, error)
	Connector(id uint32) (kdrm.Connector, error)
	Encoder(id uint32) (kdrm.Encoder, error)
	Planes() ([]uint32, error)
	Plane(id uint32) (kdrm.Plane, error)
	Properties(object, kind uint32) (map[string]kdrm.Property, error)
	PropertyInfo(id uint32) (kdrm.PropertyInfo, error)
	Blob(id uint32) ([]byte, error)
	CreateBlob(data []byte) (uint32, error)
	DestroyBlob(id uint32) error
	CreateDumb(size image.Point) (kdrm.Dumb, error)
	DestroyDumb(b kdrm.Dumb) error
	AddFB2(fb kdrm.FB) (uint32, error)
	PrimeFDToHandle(fd int) (uint32, error)
	CloseHandle(handle uint32) error
	RemoveFB(id uint32) error
	CloseFB(id uint32) error
	Commit(r *kdrm.Request, flags uint32, userData uint64) error
	CRTCSequence(crtc uint32) (kdrm.Vblank, error)
}

type scanout struct {
	target       render.Target
	client       *clientFB
	fb           uint32
	fence        func() (int, error)
	painted      func(damage render.Region)
	prepare      func() error
	prepareLater func(done func(error) error) error
	syncRead     bool
	release      func() error
	presented    uint64
	started      time.Duration
}

func (s *scanout) free(dev device) error {
	if s.client != nil {
		s.client.leave()
		return s.release()
	}
	return errors.Join(dev.RemoveFB(s.fb), s.release())
}

func (s *scanout) keepOnScreen(dev device) error {
	if s.client != nil {
		return errors.Join(s.client.keepOnScreen(), s.release())
	}
	return errors.Join(dev.CloseFB(s.fb), s.release())
}

type allocator interface {
	renderer() render.Renderer
	renderable(format fourcc.Format) ([]fourcc.Modifier, error)
	allocate(size image.Point, l layout) (*scanout, error)
	close() error
}

type kmsObject struct {
	id    uint32
	props map[string]kdrm.Property
}

func (k kmsObject) set(r *kdrm.Request, name string, value uint64) {
	if p, ok := k.props[name]; ok {
		r.Add(k.id, p.ID, value)
	}
}

type crtc struct {
	kmsObject
	index  int
	output *Output
}

type planeKind uint8

const (
	primaryPlane planeKind = iota
	cursorPlane
	overlayPlane
)

type plane struct {
	kmsObject
	kind      planeKind
	possible  uint32
	modifiers []fourcc.Modifier
	deep      []layout
	clients   []gpu.FormatModifier
	async     []gpu.FormatModifier
	taken     bool
}

func (p *plane) layouts(d backend.BitDepth) []layout {
	if d == backend.Depth10 {
		return p.deep
	}
	return []layout{{format: scanoutFormat, modifiers: p.modifiers}}
}

var (
	_ backend.Scanner      = (*Output)(nil)
	_ backend.Inheriting   = (*Output)(nil)
	_ backend.FrameTearing = (*Output)(nil)
)

type Backend struct {
	loop         *loop.Loop
	modifiers    bool
	async        bool
	number       uint64
	driver       string
	vendors      edid.Vendors
	report       func(error)
	dev          device
	alloc        allocator
	handler      backend.Handler
	crtcs        []*crtc
	planes       []*plane
	outputs      []*Output
	src          *loop.Source
	req          kdrm.Request
	events       []byte
	cursor       image.Point
	paused       bool
	closed       bool
	clientFBs    map[backend.ScanoutKey]*clientFB
	unimportable map[backend.ScanoutKey]error
	probes       sync.WaitGroup
	syncReads    uint64
	probing      bool
	probeAgain   bool
	starved      bool
	fenceTime    func(fd int) (time.Duration, bool, error)
	now          func() time.Duration
}

type clientFB struct {
	dev       device
	report    func(error)
	fb        uint32
	shown     int
	forgotten bool
	kept      bool
}

func (cf *clientFB) leave() {
	cf.shown--
	if cf.forgotten && cf.shown == 0 {
		cf.remove()
	}
}

func (cf *clientFB) remove() {
	if cf.kept {
		return
	}
	if err := cf.dev.RemoveFB(cf.fb); err != nil {
		cf.report(fmt.Errorf("drm: a client buffer's framebuffer %d stays allocated: %w", cf.fb, err))
	}
}

func (cf *clientFB) keepOnScreen() error {
	if cf.kept {
		return nil
	}
	cf.kept = true
	return cf.dev.CloseFB(cf.fb)
}

func (b *Backend) clientFB(key backend.ScanoutKey, d gpu.DMABUF) (*clientFB, error) {
	if cf, ok := b.clientFBs[key]; ok {
		return cf, nil
	}
	if err, ok := b.unimportable[key]; ok {
		return nil, err
	}
	fb, err := importFB(b.dev, d, b.modifiers)
	if err != nil {
		b.unimportable[key] = err
		return nil, err
	}
	cf := &clientFB{dev: b.dev, report: b.report, fb: fb}
	b.clientFBs[key] = cf
	return cf, nil
}

func (b *Backend) forget(key backend.ScanoutKey) {
	delete(b.unimportable, key)
	cf, ok := b.clientFBs[key]
	if !ok {
		return
	}
	delete(b.clientFBs, key)
	b.forgetTests(cf.fb)
	cf.forgotten = true
	if cf.shown == 0 {
		cf.remove()
	}
}

func newBackend(l *loop.Loop, dev device, alloc allocator, opts Options) (*Backend, error) {
	if opts.Report == nil {
		return nil, errors.New("drm: Options.Report is required")
	}
	if opts.Master > WithoutMaster {
		return nil, fmt.Errorf("drm: Options.Master %d is neither HoldsMaster nor WithoutMaster", opts.Master)
	}
	b := &Backend{loop: l, dev: dev, alloc: alloc, vendors: opts.Vendors, report: opts.Report, paused: opts.Master == WithoutMaster, events: make([]byte, eventBuffer), clientFBs: map[backend.ScanoutKey]*clientFB{}, unimportable: map[backend.ScanoutKey]error{}, fenceTime: kdrm.SyncFileSignalTime, now: backend.Now}
	if a, ok := alloc.(*gbmAllocator); ok && !a.gpu.NativeFences() {
		opts.Report(fmt.Errorf("drm: %T has no native fences, so every frame blocks the compositor in glFinish until the GPU is done", a.gpu))
	}
	modifiers, err := dev.Cap(kdrm.CapAddFB2Modifiers)
	if err != nil {
		return nil, fmt.Errorf("drm: ADDFB2 modifier support: %w", err)
	}
	b.modifiers = modifiers == 1
	if a, ok := alloc.(attachable); ok {
		a.attach(l, b.modifiers)
	}
	async, err := dev.Cap(kdrm.CapAtomicAsyncFlip)
	if err != nil && !errors.Is(err, unix.EINVAL) {
		return nil, fmt.Errorf("drm: atomic async page flip support: %w", err)
	}
	b.async = async == 1
	if b.number, err = dev.Number(); err != nil {
		return nil, err
	}
	b.driver = driverOf(b.number)
	res, err := dev.Resources()
	if err != nil {
		return nil, err
	}
	for i, id := range res.CRTCs {
		props, err := dev.Properties(id, kdrm.ObjectCRTC)
		if err != nil {
			return nil, err
		}
		b.crtcs = append(b.crtcs, &crtc{kmsObject: kmsObject{id, props}, index: i})
	}
	planes, err := dev.Planes()
	if err != nil {
		return nil, err
	}
	renderable, err := alloc.renderable(scanoutFormat)
	if err != nil {
		return nil, err
	}
	deepRenderable := make([]layout, 0, len(deepFormats))
	for _, f := range deepFormats {
		mods, err := alloc.renderable(f)
		if err != nil {
			return nil, err
		}
		deepRenderable = append(deepRenderable, layout{format: f, modifiers: mods})
	}
	var size [2]uint64
	for i, c := range []uint64{kdrm.CapCursorWidth, kdrm.CapCursorHeight} {
		if size[i], err = dev.Cap(c); err != nil {
			return nil, fmt.Errorf("drm: cursor size: %w", err)
		}
	}
	b.cursor = image.Pt(int(size[0]), int(size[1])) //nolint:gosec // cursor sizes are a few hundred pixels
	for _, id := range planes {
		p, err := b.plane(id, renderable, deepRenderable)
		if err != nil {
			return nil, err
		}
		if p != nil {
			b.planes = append(b.planes, p)
		}
	}
	if b.src, err = l.AddFD(dev.FD(), loop.Readable, b.readEvents); err != nil {
		return nil, err
	}
	return b, nil
}

func driverOf(number uint64) string {
	link, err := os.Readlink(fmt.Sprintf("/sys/dev/char/%d:%d/device/driver", unix.Major(number), unix.Minor(number)))
	if err != nil {
		return ""
	}
	return filepath.Base(link)
}

func deepLayouts(formats []fourcc.Format, blob []byte, renderable []layout) ([]layout, error) {
	var layouts []layout
	for _, r := range renderable {
		if !slices.Contains(formats, r.format) {
			continue
		}
		if blob == nil {
			layouts = append(layouts, layout{format: r.format})
			continue
		}
		scanable, err := kdrm.Modifiers(blob, r.format)
		if err != nil {
			return nil, err
		}
		both := layout{format: r.format}
		for _, m := range scanable {
			if slices.Contains(r.modifiers, m) {
				both.modifiers = append(both.modifiers, m)
			}
		}
		if len(both.modifiers) > 0 {
			layouts = append(layouts, both)
		}
	}
	return layouts, nil
}

func (b *Backend) plane(id uint32, renderable []fourcc.Modifier, deepRenderable []layout) (*plane, error) {
	info, err := b.dev.Plane(id)
	if err != nil {
		return nil, err
	}
	props, err := b.dev.Properties(id, kdrm.ObjectPlane)
	if err != nil {
		return nil, err
	}
	kind, ok := props["type"]
	if !ok {
		return nil, fmt.Errorf("drm: plane %d has no type property", id)
	}
	enum, err := b.dev.PropertyInfo(kind.ID)
	if err != nil {
		return nil, err
	}
	p := &plane{kmsObject: kmsObject{id, props}, possible: info.PossibleCRTCs}
	var format fourcc.Format
	switch kind.Value {
	case enum.Enums["Primary"]:
		p.kind, format = primaryPlane, scanoutFormat
	case enum.Enums["Cursor"]:
		p.kind, format = cursorPlane, cursorFormat
	case enum.Enums["Overlay"]:
		if _, stacked := props["zpos"]; !stacked {
			return nil, nil
		}
		p.kind, format = overlayPlane, scanoutFormat
	default:
		return nil, nil
	}
	if !slices.Contains(info.Formats, format) {
		return nil, nil
	}
	var blob []byte
	if in, ok := props["IN_FORMATS"]; ok && in.Value != 0 {
		if blob, err = b.dev.Blob(uint32(in.Value)); err != nil { //nolint:gosec // property blob IDs are 32-bit
			return nil, err
		}
	}
	if p.kind == overlayPlane {
		if p.clients, err = b.clientFormats(info.Formats, blob); err != nil {
			return nil, fmt.Errorf("drm: formats of overlay plane %d: %w", id, err)
		}
		return p, nil
	}
	if p.kind == primaryPlane {
		if p.clients, err = b.clientFormats(info.Formats, blob); err != nil {
			return nil, fmt.Errorf("drm: formats of primary plane %d: %w", id, err)
		}
		if in, ok := props["IN_FORMATS_ASYNC"]; ok && in.Value != 0 {
			async, err := b.dev.Blob(uint32(in.Value)) //nolint:gosec // property blob IDs are 32-bit
			if err != nil {
				return nil, err
			}
			if p.async, err = b.clientFormats(info.Formats, async); err != nil {
				return nil, fmt.Errorf("drm: async flip formats of primary plane %d: %w", id, err)
			}
		}
	}
	if p.kind == primaryPlane {
		if p.deep, err = deepLayouts(info.Formats, blob, deepRenderable); err != nil {
			return nil, fmt.Errorf("drm: 10-bit formats of primary plane %d: %w", id, err)
		}
	}
	if blob == nil {
		return p, nil
	}
	scanable, err := kdrm.Modifiers(blob, format)
	if err != nil {
		return nil, err
	}
	if p.kind == cursorPlane {
		if !slices.Contains(scanable, fourcc.Linear) {
			return nil, nil
		}
		return p, nil
	}
	for _, m := range scanable {
		if slices.Contains(renderable, m) {
			p.modifiers = append(p.modifiers, m)
		}
	}
	if len(p.modifiers) == 0 {
		return nil, fmt.Errorf("drm: primary plane %d scans out no XRGB8888 modifier the renderer can draw: plane %v, renderer %v", id, scanable, renderable)
	}
	return p, nil
}

func (b *Backend) clientFormats(formats []fourcc.Format, inFormats []byte) ([]gpu.FormatModifier, error) {
	var pairs []gpu.FormatModifier
	for _, f := range formats {
		modifiers := []fourcc.Modifier{fourcc.Invalid}
		if inFormats != nil {
			var err error
			if modifiers, err = kdrm.Modifiers(inFormats, f); err != nil {
				return nil, err
			}
		}
		for _, m := range modifiers {
			if addable(m, b.modifiers) {
				pairs = append(pairs, gpu.FormatModifier{Format: f, Modifier: m})
			}
		}
	}
	return pairs, nil
}

func (b *Backend) Renderer() render.Renderer {
	return b.alloc.renderer()
}

func (b *Backend) Start(h backend.Handler) error {
	if b.handler != nil {
		return errors.New("drm: already started")
	}
	b.handler = h
	return b.scan()
}

func (b *Backend) scan() error {
	return b.applyProbe(probeConnectors(b.dev))
}

type probedConnector struct {
	c   kdrm.Connector
	err error
}

type connectorProbe struct {
	ids        []uint32
	connectors map[uint32]probedConnector
	err        error
}

func probeConnectors(dev device) connectorProbe {
	res, err := dev.Resources()
	if err != nil {
		return connectorProbe{err: err}
	}
	p := connectorProbe{ids: res.Connectors, connectors: make(map[uint32]probedConnector, len(res.Connectors))}
	for _, id := range res.Connectors {
		c, err := dev.Connector(id)
		p.connectors[id] = probedConnector{c: c, err: err}
	}
	return p
}

func (b *Backend) rescan() error {
	if b.probing {
		b.probeAgain = true
		return nil
	}
	b.probing = true
	b.probes.Go(func() {
		p := probeConnectors(b.dev)
		if err := b.loop.Post(func() error { return b.probed(p) }); err != nil && !errors.Is(err, loop.ErrClosed) {
			panic(fmt.Sprintf("drm: hand a connector probe back to the loop: %v", err))
		}
	})
	return nil
}

func (b *Backend) probed(p connectorProbe) error {
	b.probing = false
	if b.closed {
		return nil
	}
	if b.probeAgain {
		b.probeAgain = false
		return b.rescan()
	}
	if b.paused {
		return nil
	}
	return b.applyProbe(p)
}

func (b *Backend) applyProbe(p connectorProbe) error {
	if p.err != nil {
		return p.err
	}
	b.starved = false
	connected := make([]kdrm.Connector, 0, len(p.ids))
	for _, id := range p.ids {
		if probed := p.connectors[id]; probed.err == nil && probed.c.Connection == kdrm.Connected {
			c := probed.c
			c.Modes = progressive(c.Modes)
			connected = append(connected, c)
		}
	}
	groups, err := b.tileGroups(connected)
	errs := []error{err}
	for _, o := range slices.Clone(b.outputs) {
		if !slices.Contains(p.ids, o.conn.ID) {
			errs = append(errs, b.remove(o))
			continue
		}
		probed := p.connectors[o.conn.ID]
		c, err := probed.c, probed.err
		switch {
		case errors.Is(err, unix.ENOENT):
			errs = append(errs, b.remove(o))
		case err != nil:
			errs = append(errs, err, b.remove(o))
		case c.Connection != kdrm.Connected || !o.keepsShape(connected, groups):
			errs = append(errs, b.remove(o))
		default:
			o.bpcRefused = false
			errs = append(errs, o.recheckLink(c))
		}
	}
	for _, id := range p.ids {
		if slices.ContainsFunc(b.outputs, func(o *Output) bool { return o.ownsConnector(id) }) {
			continue
		}
		probed := p.connectors[id]
		c, err := probed.c, probed.err
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		c.Modes = progressive(c.Modes)
		if c.Connection != kdrm.Connected || len(c.Modes) == 0 {
			continue
		}
		g := groupOf(groups, id)
		switch {
		case g == nil:
			errs = append(errs, b.add(c, nil))
		case g.whole != nil:
			if g.whole.c.ID == id {
				errs = append(errs, b.add(c, nil))
			}
		case g.members[0].c.ID == id:
			errs = append(errs, b.addTiled(g))
		}
	}
	return errors.Join(errs...)
}

func groupOf(groups map[uint32]*tileGroup, id uint32) *tileGroup {
	for _, g := range groups {
		if g.owns(id) {
			return g
		}
	}
	return nil
}

func (o *Output) keepsShape(connected []kdrm.Connector, groups map[uint32]*tileGroup) bool {
	g := groupOf(groups, o.conn.ID)
	if !o.tiled() {
		return g == nil || g.whole != nil && g.whole.c.ID == o.conn.ID
	}
	if g == nil || g.whole != nil || g.members[0].c.ID != o.conn.ID {
		return false
	}
	for _, t := range o.tiles {
		if !g.owns(t.conn.ID) || !slices.ContainsFunc(connected, func(c kdrm.Connector) bool { return c.ID == t.conn.ID }) {
			return false
		}
	}
	return true
}

func (b *Backend) addTiled(g *tileGroup) error {
	c, lead, tiles, err := b.newTiles(g)
	if err != nil {
		b.report(fmt.Errorf("%w; its tiles light as separate outputs", err))
		errs := make([]error, 0, len(g.members))
		for _, m := range g.members {
			errs = append(errs, b.add(m.c, nil))
		}
		return errors.Join(errs...)
	}
	return b.add(c, &tiling{mode: lead, tiles: tiles})
}

func progressive(modes []kdrm.Mode) []kdrm.Mode {
	return slices.DeleteFunc(slices.Clone(modes), func(m kdrm.Mode) bool { return m.Flags&kdrm.ModeFlagInterlace != 0 })
}

func (b *Backend) SyncReads() uint64 {
	return b.syncReads
}

func (b *Backend) Hotplug() error {
	if b.paused {
		return nil
	}
	return b.rescan()
}

func (b *Backend) Attach(h backend.Handler) error {
	if b.handler != nil {
		return errors.New("drm: already started")
	}
	b.handler = h
	return b.rescan()
}

func (b *Backend) frees(kind planeKind, cr *crtc) iter.Seq[*plane] {
	return func(yield func(*plane) bool) {
		for _, p := range b.planes {
			if p.kind == kind && !p.taken && p.possible&(1<<cr.index) != 0 && !yield(p) {
				return
			}
		}
	}
}

func (b *Backend) free(kind planeKind, cr *crtc) *plane {
	for p := range b.frees(kind, cr) {
		return p
	}
	return nil
}

func (b *Backend) pick(c kdrm.Connector, routed uint32) (*crtc, *plane, error) {
	var first *crtc
	for _, encID := range c.Encoders {
		enc, err := b.dev.Encoder(encID)
		if err != nil {
			return nil, nil, err
		}
		for _, cr := range b.crtcs {
			if cr.output != nil || enc.PossibleCRTCs&(1<<cr.index) == 0 || b.free(primaryPlane, cr) == nil {
				continue
			}
			if cr.id == routed {
				p, err := b.primaryOn(cr)
				return cr, p, err
			}
			if first == nil {
				first = cr
			}
		}
	}
	if first == nil {
		return nil, nil, fmt.Errorf("drm: %s: %w", c.Name(), errNoCRTC)
	}
	return first, b.free(primaryPlane, first), nil
}

func (b *Backend) primaryOn(cr *crtc) (*plane, error) {
	for p := range b.frees(primaryPlane, cr) {
		on, err := b.scanning(p)
		if err != nil {
			return nil, err
		}
		if on == cr.id {
			return p, nil
		}
	}
	return b.free(primaryPlane, cr), nil
}

func (b *Backend) scanning(p *plane) (uint32, error) {
	props, err := b.dev.Properties(p.id, kdrm.ObjectPlane)
	if err != nil {
		return 0, fmt.Errorf("drm: read what plane %d scans out: %w", p.id, err)
	}
	if props["FB_ID"].Value == 0 {
		return 0, nil
	}
	return uint32(props["CRTC_ID"].Value), nil //nolint:gosec // KMS object IDs are 32-bit
}

func (b *Backend) inherits(o *Output, routed uint32) (bool, error) {
	if routed != o.crtc.id || o.tiled() {
		return false, nil
	}
	props, err := b.dev.Properties(o.crtc.id, kdrm.ObjectCRTC)
	if err != nil {
		return false, fmt.Errorf("drm: read the state of CRTC %d for %s: %w", o.crtc.id, o.Name(), err)
	}
	blob := props["MODE_ID"].Value
	if props["ACTIVE"].Value != 1 || blob == 0 {
		return false, nil
	}
	data, err := b.dev.Blob(uint32(blob)) //nolint:gosec // property blob IDs are 32-bit
	if err != nil {
		return false, fmt.Errorf("drm: read the lit mode of CRTC %d for %s: %w", o.crtc.id, o.Name(), err)
	}
	lit, err := kdrm.ParseMode(data)
	if err != nil {
		return false, fmt.Errorf("drm: the lit mode of CRTC %d for %s: %w", o.crtc.id, o.Name(), err)
	}
	if !sameTimings(lit, o.mode) {
		return false, nil
	}
	on, err := b.scanning(o.primary)
	return on == o.crtc.id, err
}

func sameTimings(a, b kdrm.Mode) bool {
	a.Type, a.Name, b.Type, b.Name = 0, [32]byte{}, 0, [32]byte{}
	return a == b
}

func (b *Backend) add(c kdrm.Connector, t *tiling) error {
	err := b.light(c, t)
	if t != nil {
		err = errors.Join(err, t.release(b.dev))
	}
	return err
}

func (b *Backend) light(c kdrm.Connector, t *tiling) error {
	props, err := b.dev.Properties(c.ID, kdrm.ObjectConnector)
	if err != nil {
		return err
	}
	if props["non-desktop"].Value != 0 {
		b.report(fmt.Errorf("drm: %s is a non-desktop display, such as a VR headset, and is not lit as an output", c.Name()))
		return nil
	}
	routed := uint32(props["CRTC_ID"].Value) //nolint:gosec // KMS object IDs are 32-bit
	cr, p, err := b.pick(c, routed)
	if errors.Is(err, errNoCRTC) {
		b.starved = true
		b.report(fmt.Errorf("%w; it lights when another output on the device is disabled or unplugged", err))
		return nil
	}
	if err != nil {
		return err
	}
	o := &Output{b: b, conn: c, connector: kmsObject{c.ID, props}, renderFence: -1, lateFence: -1, tested: map[planeKey]bool{}}
	if t != nil {
		o.tiles, o.tileMode, t.tiles = t.tiles, t.mode, nil
	}
	o.clock = backend.NewFrameClock(b.loop.Idle, o.frameReady, o.runFrame, &backend.Latch{
		Wait: o.frameWait,
		Wake: func(after time.Duration) error {
			o.frameDue = o.b.now() + after
			return o.latch.Arm(after)
		},
	})
	o.colorErr = errNoEDID
	if blob, ok := props["EDID"]; ok && blob.Value != 0 {
		data, err := b.dev.Blob(uint32(blob.Value)) //nolint:gosec // property blob IDs are 32-bit
		if err != nil {
			return err
		}
		if info, err := edid.Parse(data, b.vendors); err != nil {
			b.report(fmt.Errorf("drm: %s has an EDID lys-wm cannot read, so only its connector names it: %w", c.Name(), err))
		} else {
			o.identity = info
		}
		o.colorEDID, o.colorErr = edid.ParseHDR(data)
	}
	o.bpcFound = props[propMaxBPC].Value
	o.bpc = o.bpcFound
	o.bind(cr, p)
	if err := o.bindTiles(); err != nil {
		err = errors.Join(err, o.release())
		if errors.Is(err, errNoCRTC) {
			b.starved = true
			b.report(fmt.Errorf("%w; it lights when another output on the device is disabled or unplugged", err))
			return nil
		}
		return err
	}
	if o.pacer, err = b.loop.AddTimer(o.paceElapsed); err != nil {
		return errors.Join(err, o.release())
	}
	if o.latch, err = b.loop.AddTimer(o.clock.Woke); err != nil {
		return errors.Join(err, o.release())
	}
	if o.planesLatch, err = b.loop.AddTimer(o.planesDue); err != nil {
		return errors.Join(err, o.release())
	}
	if o.commitLatch, err = b.loop.AddTimer(o.lateDue); err != nil {
		return errors.Join(err, o.release())
	}
	if err := o.lightBest(); err != nil {
		err = errors.Join(err, o.release())
		if refused(err) {
			b.report(err)
			return nil
		}
		return err
	}
	inherited, err := b.inherits(o, routed)
	if err != nil {
		return errors.Join(err, o.release())
	}
	if inherited {
		o.kms, o.lit = kmsInherited, true
	}
	b.outputs = append(b.outputs, o)
	return b.handler.OutputAdded(o)
}

func (b *Backend) remove(o *Output) error {
	i := slices.Index(b.outputs, o)
	if i < 0 {
		return nil
	}
	b.outputs = slices.Delete(b.outputs, i, i+1)
	return errors.Join(b.handler.OutputRemoved(o), o.disable(), o.release())
}

func (b *Backend) Outputs() []*Output {
	return slices.Clone(b.outputs)
}

func (b *Backend) readEvents(loop.Events) error {
	for {
		n, err := unix.Read(b.dev.FD(), b.events)
		switch {
		case errors.Is(err, unix.EAGAIN):
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return fmt.Errorf("drm: read events: %w", err)
		case n == 0:
			return nil
		}
		if err := kdrm.ParseEvents(b.events[:n], b.flipped); err != nil {
			return err
		}
		if n < len(b.events) {
			return nil
		}
	}
}

func (b *Backend) flipped(e kdrm.FlipEvent) error {
	for _, o := range b.outputs {
		if o.bound() && uint64(o.crtc.id) == e.UserData {
			return errors.Join(o.flipped(e), o.commitPipelined(), b.commitHeld())
		}
	}
	return nil
}

func (b *Backend) Paused() bool {
	return b.paused
}

func (b *Backend) Pause() {
	b.paused = true
}

func (b *Backend) Wake() error {
	var errs []error
	for _, o := range b.outputs {
		o.kms = kmsModeset
		errs = append(errs, b.handler.OutputDamaged(o))
	}
	return errors.Join(errs...)
}

func (b *Backend) Resume() error {
	b.paused = false
	var errs []error
	for _, o := range b.outputs {
		o.kms, o.flipping, o.busy, o.offDue, o.offBusy = kmsModeset, false, nil, o.offDue || o.offBusy, false
		errs = append(errs, o.dropLate(), o.dropScanout(o.queued))
		o.queued = nil
		if o.cursor != nil {
			o.cursor.queued = nil
		}
		o.powerOffAfterFlip()
		errs = append(errs, o.bpcLeft(), b.handler.OutputDamaged(o))
	}
	return errors.Join(append(errs, b.rescan())...)
}

func (b *Backend) Unplug() error {
	if b.closed {
		return nil
	}
	b.closed = true
	b.probes.Wait()
	var errs []error
	for len(b.outputs) > 0 {
		o := b.outputs[0]
		b.outputs = b.outputs[1:]
		if b.handler != nil {
			errs = append(errs, b.handler.OutputRemoved(o))
		}
		errs = append(errs, o.release())
	}
	if b.src != nil {
		errs = append(errs, b.src.Remove())
	}
	return errors.Join(append(errs, b.alloc.close())...)
}

func (b *Backend) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	b.probes.Wait()
	var errs []error
	for len(b.outputs) > 0 {
		o := b.outputs[0]
		b.outputs = b.outputs[1:]
		errs = append(errs, b.handler.OutputRemoved(o), o.leaveOnScreen(), o.release())
	}
	for key := range b.clientFBs {
		b.forget(key)
	}
	if b.src != nil {
		errs = append(errs, b.src.Remove())
	}
	return errors.Join(append(errs, b.alloc.close())...)
}

type kmsState uint8

const (
	kmsModeset kmsState = iota
	kmsInherited
	kmsFlip
)

type Output struct {
	spare         []*scanout
	overlay       *plane
	overlayNext   *overlayUse
	overlayQueued *overlayUse
	overlayShown  *overlayUse
	overlayTested map[overlayTest]bool
	damageBlob    uint32
	damageRects   render.Region
	damageBytes   []byte
	offDue        bool
	b             *Backend
	identity      edid.Info
	conn          kdrm.Connector
	connector     kmsObject
	crtc          *crtc
	primary       *plane
	cursor        *cursor
	mode          kdrm.Mode
	depth         backend.BitDepth
	modeBlob      uint32
	enabled       bool
	off           bool
	kms           kmsState
	lit           bool
	buffers       []*scanout
	drawing       *scanout
	queued        *scanout
	front         *scanout
	clock         *backend.FrameClock
	flipping      bool
	framing       bool
	pacer         *loop.Timer
	planesLatch   *loop.Timer
	planesWaiting bool
	latch         *loop.Timer
	frameDue      time.Duration
	empties       int
	idleAt        time.Duration
	budget        backend.RenderBudget
	tested        map[planeKey]bool
	frameStart    time.Duration
	renderStart   time.Duration
	layout        layout
	renderFence   int
	aimedAt       time.Duration
	untimed       bool
	lastFlip      time.Duration
	unanchored    bool
	presented     uint64
	color         colorBlobs
	signal        signal
	colorEDID     edid.HDR
	colorErr      error
	torn          bool
	planesFailing bool
	restate       bool
	restated      []uint32
	extraFlips    int
	busy          *scanout
	offBusy       bool
	bpcFound      uint64
	bpc           uint64
	bpcRefused    bool
	tiles         []*tile
	tileMode      kdrm.Mode
	restatedFlips int
	contentType   wm.ContentType
	contentDirty  bool
	vrr           bool
	vrrSent       bool
	tearFrame     bool
	commitLatch   *loop.Timer
	late          *scanout
	lateFence     int
	lateTimed     bool
}

func (o *Output) Name() string {
	return o.conn.Name()
}

func (o *Output) Description() string {
	if d := o.identity.Description(); d != "" {
		return d
	}
	return o.Name()
}

func (o *Output) Identity() edid.Info {
	return o.identity
}

func (o *Output) PhysicalSize() image.Point {
	return image.Pt(int(o.conn.PhysicalMM[0]), int(o.conn.PhysicalMM[1]))
}

func modeOf(m kdrm.Mode) backend.Mode {
	return backend.Mode{Size: image.Pt(int(m.HDisplay), int(m.VDisplay)), RefreshMHz: m.RefreshMHz(), Preferred: m.Preferred()}
}

func (o *Output) Modes() []backend.Mode {
	out := make([]backend.Mode, len(o.conn.Modes))
	for i, m := range o.conn.Modes {
		out[i] = modeOf(m)
	}
	return out
}

func (o *Output) State() backend.OutputState {
	power := backend.PoweredOn
	switch {
	case !o.enabled:
		power = backend.Disabled
	case o.off:
		power = backend.PoweredOff
	}
	return backend.OutputState{Power: power, Mode: modeOf(o.mode), Depth: o.depth, HDR: o.signal.meta}
}

func (o *Output) setMode(m kdrm.Mode, d backend.BitDepth, hdr backend.HDRMetadata) error {
	layouts := o.primary.layouts(d)
	if len(layouts) == 0 {
		return fmt.Errorf("drm: the primary plane of %s scans out none of %v that the renderer can draw: %w", o.Name(), deepFormats, backend.ErrDepthUnsupported)
	}
	next, err := o.newSignal(hdr)
	if err != nil {
		return err
	}
	blob, err := o.b.dev.CreateBlob(o.crtcMode(m).Bytes())
	if err != nil {
		return errors.Join(err, o.dropSignal(next))
	}
	size := image.Pt(int(m.HDisplay), int(m.VDisplay))
	refusals := make([]error, 0, len(layouts))
	for _, l := range layouts {
		buffers, restate, err := o.tryLayout(m, d, blob, size, l, next)
		if own, how, ok := o.b.fallBack(l, err); ok {
			o.b.report(fmt.Errorf("drm: %s in %+v scans out no linear buffer of the primary GPU, so its frames take %s: %w", o.Name(), modeOf(m), how, err))
			l = own
			buffers, restate, err = o.tryLayout(m, d, blob, size, l, next)
		}
		if err != nil {
			refusals = append(refusals, err)
			continue
		}
		if len(refusals) > 0 {
			o.b.report(fmt.Errorf("drm: %s draws %v frames because earlier formats were refused: %w", o.Name(), l.format, errors.Join(refusals...)))
		}
		return o.adoptMode(m, d, l, blob, buffers, next, restate)
	}
	refusals = append(refusals, o.b.dev.DestroyBlob(blob), o.dropSignal(next))
	if hdr.Active {
		return fmt.Errorf("drm: %s takes no HDR signal on any of the 10-bit formats its primary plane offers: %w: %w", o.Name(), backend.ErrHDRUnsupported, errors.Join(refusals...))
	}
	if d == backend.Depth8 {
		return fmt.Errorf("drm: %s keeps its current frames: %w", o.Name(), errors.Join(refusals...))
	}
	return fmt.Errorf("drm: %s takes none of the 10-bit formats its primary plane offers: %w: %w", o.Name(), backend.ErrDepthUnsupported, errors.Join(refusals...))
}

func (o *Output) tryLayout(m kdrm.Mode, d backend.BitDepth, blob uint32, size image.Point, l layout, next signal) ([]*scanout, bool, error) {
	buffers, err := o.allocateBuffers(size, l)
	if err != nil {
		return nil, false, fmt.Errorf("%v buffers: %w", l.format, err)
	}
	if o.b.paused {
		return buffers, false, nil
	}
	restate, err := o.testMode(m, d, blob, buffers[0], next)
	if err != nil {
		return nil, false, errors.Join(fmt.Errorf("%v frames: %w", l.format, err), o.free(buffers))
	}
	return buffers, restate, nil
}

type fallingBack interface {
	fallBack(l layout, err error) (layout, string, bool)
}

func (b *Backend) fallBack(l layout, err error) (layout, string, bool) {
	f, ok := b.alloc.(fallingBack)
	if !ok || err == nil {
		return l, "", false
	}
	return f.fallBack(l, err)
}

func (o *Output) allocateBuffers(size image.Point, l layout) ([]*scanout, error) {
	buffers := make([]*scanout, 0, buffersPerOut)
	for range buffersPerOut {
		s, err := o.b.alloc.allocate(size, l)
		if err != nil {
			return nil, errors.Join(err, o.free(buffers))
		}
		buffers = append(buffers, s)
	}
	return buffers, nil
}

func (o *Output) free(buffers []*scanout) error {
	errs := make([]error, 0, len(buffers))
	for _, s := range buffers {
		errs = append(errs, s.free(o.b.dev))
	}
	return errors.Join(errs...)
}

func (o *Output) testMode(m kdrm.Mode, d backend.BitDepth, blob uint32, s *scanout, next signal) (bool, error) {
	mode, depth, modeBlob, kms, sig, restate := o.mode, o.depth, o.modeBlob, o.kms, o.signal, o.restate
	defer func() {
		o.mode, o.depth, o.modeBlob, o.kms, o.signal, o.restate = mode, depth, modeBlob, kms, sig, restate
	}()
	o.mode, o.depth, o.modeBlob, o.kms, o.signal, o.restate = m, d, blob, kmsModeset, next, false
	o.frame(s, -1)
	alone := o.test(kdrm.AtomicAllowModeset)
	if refused(alone) && o.bpcChanges() && o.maxBPC() > o.bpcFound {
		bpc := o.maxBPC()
		o.bpcRefused = true
		o.frame(s, -1)
		if err := o.test(kdrm.AtomicAllowModeset); err == nil {
			o.b.report(fmt.Errorf("drm: %s refuses max bpc %d, so its 10-bit frames may reach the panel dithered: %w", o.Name(), bpc, alone))
			return false, nil
		}
		o.bpcRefused = false
	}
	if !refused(alone) || len(o.b.litBeside(o)) == 0 {
		return false, alone
	}
	o.restate = true
	o.frame(s, -1)
	all := o.test(kdrm.AtomicAllowModeset)
	if err := o.dropRestated(); err != nil {
		return false, errors.Join(alone, all, err)
	}
	if all != nil {
		return false, fmt.Errorf("%w; with every lit output restated: %w", alone, all)
	}
	return true, nil
}

func (b *Backend) litBeside(o *Output) []*Output {
	var lit []*Output
	for _, l := range b.outputs {
		if l != o && l.enabled && l.lit && !l.off && l.bound() {
			lit = append(lit, l)
		}
	}
	return lit
}

func (o *Output) addRestatement(r *kdrm.Request) {
	if err := o.dropRestated(); err != nil {
		o.b.report(fmt.Errorf("drm: release the restated modes beside %s: %w", o.Name(), err))
	}
	for _, l := range o.b.litBeside(o) {
		blob, err := o.b.dev.CreateBlob(l.crtcMode(l.mode).Bytes())
		if err != nil {
			o.b.report(fmt.Errorf("drm: restate the mode of %s beside %s: %w", l.Name(), o.Name(), err))
			continue
		}
		o.restated = append(o.restated, blob)
		o.restatedFlips += 1 + len(l.tiles)
		l.addModeset(r, blob)
	}
}

func (o *Output) dropRestated() error {
	errs := make([]error, 0, len(o.restated))
	for _, blob := range o.restated {
		errs = append(errs, o.b.dev.DestroyBlob(blob))
	}
	o.restated, o.restatedFlips = o.restated[:0], 0
	return errors.Join(errs...)
}

func (o *Output) lightBest() error {
	preferred := o.conn.Modes[max(slices.IndexFunc(o.conn.Modes, kdrm.Mode.Preferred), 0)]
	first := o.setMode(preferred, backend.Depth8, backend.HDRMetadata{})
	if !refused(first) {
		return first
	}
	refusals := []error{fmt.Errorf("%+v: %w", modeOf(preferred), first)}
	for _, m := range stepDown(o.conn.Modes, preferred) {
		err := o.setMode(m, backend.Depth8, backend.HDRMetadata{})
		if err == nil {
			o.b.report(fmt.Errorf("drm: %s lights in %+v, the best mode the device takes: %w", o.Name(), modeOf(m), errors.Join(refusals...)))
			return nil
		}
		if !refused(err) {
			return err
		}
		refusals = append(refusals, fmt.Errorf("%+v: %w", modeOf(m), err))
	}
	return fmt.Errorf("drm: %s stays dark, the device takes none of its modes: %w", o.Name(), errors.Join(refusals...))
}

func stepDown(modes []kdrm.Mode, refusedMode kdrm.Mode) []kdrm.Mode {
	next := slices.DeleteFunc(slices.Clone(modes), func(m kdrm.Mode) bool { return m == refusedMode })
	slices.SortStableFunc(next, func(a, b kdrm.Mode) int {
		if d := int(b.HDisplay)*int(b.VDisplay) - int(a.HDisplay)*int(a.VDisplay); d != 0 {
			return d
		}
		return int(b.RefreshMHz() - a.RefreshMHz())
	})
	return next
}

func (o *Output) adoptMode(m kdrm.Mode, d backend.BitDepth, l layout, blob uint32, buffers []*scanout, next signal, restate bool) error {
	clear(o.tested)
	err := errors.Join(o.freeBuffers(), o.releaseSignal())
	o.signal = next
	if o.modeBlob != 0 {
		err = errors.Join(err, o.b.dev.DestroyBlob(o.modeBlob))
	}
	o.buffers, o.layout, o.mode, o.depth, o.modeBlob, o.enabled, o.kms, o.restate = buffers, l, m, d, blob, true, kmsModeset, restate
	return err
}

func (o *Output) Commit(s backend.OutputState) error {
	if !s.Enabled() {
		if err := o.disable(); err != nil {
			return err
		}
		return o.b.lightStarved()
	}
	i := slices.IndexFunc(o.conn.Modes, func(m kdrm.Mode) bool { return modeOf(m) == s.Mode })
	if i < 0 {
		return fmt.Errorf("drm: %s has no mode %+v", o.Name(), s.Mode)
	}
	if o.enabled && o.conn.Modes[i] == o.mode && s.Depth == o.depth && s.HDR == o.signal.meta {
		return o.setPower(s.Lit())
	}
	if !o.bound() {
		cr, p, err := o.b.pick(o.conn, 0)
		if err != nil {
			return err
		}
		o.bind(cr, p)
		if err := o.bindTiles(); err != nil {
			o.unbind()
			return err
		}
	}
	if err := o.setMode(o.conn.Modes[i], s.Depth, s.HDR); err != nil {
		if !o.enabled {
			o.unbind()
		}
		return err
	}
	o.off = !s.Lit()
	return o.ScheduleFrame()
}

const (
	propLinkStatus = "link-status"
	linkStatusGood = 0
	linkStatusBad  = 1
)

func (o *Output) recheckLink(c kdrm.Connector) error {
	if _, ok := o.connector.props[propLinkStatus]; !ok || !o.enabled || !o.lit || o.b.paused {
		return nil
	}
	props, err := o.b.dev.Properties(o.conn.ID, kdrm.ObjectConnector)
	if err != nil {
		return fmt.Errorf("drm: read the link status of %s: %w", o.Name(), err)
	}
	if props[propLinkStatus].Value != linkStatusBad {
		return nil
	}
	if fresh := progressive(c.Modes); len(fresh) > 0 && !o.tiled() {
		o.conn.Modes = fresh
	}
	m := o.mode
	if !slices.Contains(o.conn.Modes, m) {
		m = o.conn.Modes[max(slices.IndexFunc(o.conn.Modes, kdrm.Mode.Preferred), 0)]
	}
	o.b.report(fmt.Errorf("drm: the link of %s went bad; modesetting it again in %+v", o.Name(), modeOf(m)))
	if m == o.mode {
		o.kms = kmsModeset
		return o.b.handler.OutputDamaged(o)
	}
	if err := o.setMode(m, o.depth, o.signal.meta); err != nil {
		return err
	}
	return errors.Join(o.b.handler.OutputChanged(o), o.b.handler.OutputDamaged(o))
}

func (o *Output) setPower(on bool) error {
	if on == !o.off {
		return nil
	}
	if on {
		o.off, o.offDue, o.offBusy, o.kms = false, false, false, kmsModeset
		return o.b.handler.OutputDamaged(o)
	}
	if o.lit && o.flipping {
		o.off, o.offDue = true, true
		o.clock.Cancel()
		return nil
	}
	return o.powerOff()
}

func (o *Output) powerOff() error {
	if o.lit {
		r := &o.b.req
		r.Reset()
		o.crtc.set(&o.b.req, "ACTIVE", 0)
		o.detach()
		o.detachTiles(r, false)
		err := o.b.dev.Commit(r, kdrm.AtomicAllowModeset|kdrm.AtomicNonblock, 0)
		if errors.Is(err, unix.EBUSY) && o.b.flipsPending() {
			o.off, o.offBusy, o.offDue, o.busy = true, true, false, nil
			o.clock.Cancel()
			return nil
		}
		if errors.Is(err, unix.EBUSY) {
			err = o.b.dev.Commit(r, kdrm.AtomicAllowModeset, 0)
		}
		if err != nil {
			return fmt.Errorf("drm: power off %s: %w", o.Name(), err)
		}
	}
	o.off, o.flipping, o.offDue, o.offBusy, o.busy = true, false, false, false, nil
	o.clock.Cancel()
	return o.freeBuffers()
}

func (o *Output) powerOffAfterFlip() {
	if !o.offDue || o.b.paused {
		return
	}
	if err := o.powerOff(); err != nil {
		o.offDue = false
		o.b.report(fmt.Errorf("%w; the output stays lit until it next changes", err))
	}
}

func (o *Output) disable() error {
	if !o.enabled {
		return nil
	}
	if !o.lit || o.b.paused {
		o.enabled, o.lit = false, false
		err := o.freeBuffers()
		o.unbind()
		return err
	}
	r := &o.b.req
	r.Reset()
	o.connector.set(&o.b.req, "CRTC_ID", 0)
	o.crtc.set(&o.b.req, "ACTIVE", 0)
	o.crtc.set(&o.b.req, "MODE_ID", 0)
	o.detach()
	o.detachTiles(r, true)
	if err := o.commitDisable(r); err != nil {
		return fmt.Errorf("drm: disable %s: %w", o.Name(), err)
	}
	o.enabled, o.lit, o.flipping = false, false, false
	o.clock.Cancel()
	err := o.freeBuffers()
	o.unbind()
	return err
}

var errNoCRTC = errors.New("no free CRTC and primary plane")

func (o *Output) bound() bool {
	return o.crtc != nil && o.crtc.output == o
}

func (o *Output) bind(cr *crtc, p *plane) {
	o.crtc, o.primary = cr, p
	cr.output, p.taken = o, true
	clear(o.tested)
	o.overlayTested = nil
	if cp := o.b.free(cursorPlane, cr); cp != nil && !o.tiled() {
		cp.taken = true
		if o.cursor == nil {
			o.cursor = &cursor{o: o}
		}
		o.cursor.plane = cp
	} else if c := o.cursor; c != nil {
		c.refused = fmt.Errorf("drm: %s lost its cursor plane to another output while disabled", o.Name())
		if err := c.release(); err != nil {
			o.b.report(fmt.Errorf("drm: free the cursor buffers of %s: %w", o.Name(), err))
		}
		o.cursor = nil
	}
	for ov := range o.b.frees(overlayPlane, cr) {
		if o.tiled() {
			break
		}
		if ov.props["zpos"].Value > p.props["zpos"].Value {
			ov.taken = true
			o.overlay = ov
			break
		}
	}
}

func (o *Output) unbind() {
	if !o.bound() {
		return
	}
	o.crtc.output, o.primary.taken = nil, false
	o.vrrSent = false
	o.unbindTiles()
	if o.overlay != nil {
		o.overlay.taken, o.overlay = false, nil
	}
	if o.cursor != nil {
		o.cursor.plane.taken = false
	}
}

func (b *Backend) lightStarved() error {
	if !b.starved {
		return nil
	}
	b.starved = false
	return b.rescan()
}

func (o *Output) commitDisable(r *kdrm.Request) error {
	err := o.b.dev.Commit(r, kdrm.AtomicAllowModeset|kdrm.AtomicNonblock, 0)
	if !errors.Is(err, unix.EBUSY) {
		return err
	}
	return o.b.dev.Commit(r, kdrm.AtomicAllowModeset, 0)
}

func (o *Output) detach() {
	if ov := o.overlay; ov != nil && o.overlayShown != nil {
		ov.set(&o.b.req, "FB_ID", 0)
		ov.set(&o.b.req, "CRTC_ID", 0)
	}
	o.primary.set(&o.b.req, "FB_ID", 0)
	o.primary.set(&o.b.req, "CRTC_ID", 0)
	if c := o.cursor; c != nil {
		c.plane.set(&o.b.req, "FB_ID", 0)
		c.plane.set(&o.b.req, "CRTC_ID", 0)
		c.queued, c.front, c.dirty = nil, nil, true
	}
}

func (o *Output) ScheduleFrame() error {
	if !o.enabled || o.off || o.b.paused {
		return nil
	}
	return o.clock.Request()
}

func (o *Output) Inherited() bool {
	return o.kms == kmsInherited
}

func (o *Output) testable() bool {
	return o.lit && o.kms != kmsInherited && !o.off && !o.b.paused
}

func (o *Output) frameReady() bool {
	return o.enabled && (!o.flipping || o.pipelines()) && o.late == nil && o.busy == nil && o.empties < backend.EmptyFramesPerRefresh && o.drawing == nil && !o.b.paused && !o.b.closed
}

func (o *Output) pipelines() bool {
	interval := modeOf(o.mode).Interval()
	return o.kms == kmsFlip && o.queued != nil && o.queued.client == nil && o.queued.prepare == nil && o.queued.prepareLater == nil && o.budget.Overruns(interval)
}

func (o *Output) ExpectFrameWork(pixels int) {
	o.budget.Expect(pixels)
}

func (o *Output) runFrame() error {
	o.budget.Started()
	o.frameStart = o.b.now()
	o.framing = true
	err := o.b.handler.Frame(o)
	o.framing = false
	if err == nil && !o.flipping && o.busy == nil && o.enabled && !o.off {
		err = o.emptyFrame()
	}
	return errors.Join(err, o.flushPlanes())
}

func (o *Output) emptyFrame() error {
	o.empties++
	if o.empties > 1 {
		return nil
	}
	now := o.b.now()
	at, _ := backend.NextVblank(o.vblank(now), now, modeOf(o.mode).Interval())
	if err := o.pacer.Arm(at - now); err != nil {
		return fmt.Errorf("drm: pace the frames of %s: %w", o.Name(), err)
	}
	o.idleAt = at
	return nil
}

func (o *Output) paceElapsed() error {
	o.empties = 0
	var err error
	if o.queued == nil && o.enabled && !o.off {
		err = o.b.handler.Presented(o, backend.Presentation{Time: o.idleAt, Refresh: modeOf(o.mode).Interval(), Idle: true})
	}
	return errors.Join(err, o.clock.Kick(), o.flushPlanes())
}

func (o *Output) BeginFrame() (backend.Frame, error) {
	switch {
	case !o.enabled:
		return backend.Frame{}, fmt.Errorf("drm: frame on disabled %s", o.Name())
	case o.drawing != nil:
		return backend.Frame{}, backend.Broken("drm: %s already has a frame in progress", o.Name())
	}
	if len(o.buffers) == 0 {
		buffers, err := o.allocateBuffers(modeOf(o.mode).Size, o.layout)
		if err != nil {
			return backend.Frame{}, fmt.Errorf("drm: buffers for %s to draw in once it is powered on again: %w", o.Name(), err)
		}
		o.buffers = buffers
	}
	i := slices.IndexFunc(o.buffers, func(s *scanout) bool { return s != o.front && s != o.queued })
	if i < 0 && o.flipping {
		s, err := o.b.alloc.allocate(modeOf(o.mode).Size, o.layout)
		if err != nil {
			return backend.Frame{}, fmt.Errorf("drm: a buffer for %s to draw in while one flips: %w", o.Name(), err)
		}
		o.buffers, i = append(o.buffers, s), len(o.buffers)
	}
	if i < 0 {
		return backend.Frame{}, fmt.Errorf("drm: every buffer of %s is on screen or queued", o.Name())
	}
	s := o.buffers[i]
	o.drawing = s
	age := 0
	if s.presented != 0 {
		age = int(o.presented - s.presented + 1) //nolint:gosec // a couple of buffers apart
	}
	return backend.Frame{Renderer: o.b.alloc.renderer(), Target: s.target, Age: age}, nil
}

func (o *Output) AbortFrame() {
	o.drawing = nil
}

func (o *Output) Present(damage render.Region) error {
	o.budget.Painted(damage.Area())
	o.setDamageClips(damage)
	s := o.drawing
	if s == nil {
		return backend.Broken("drm: present on %s without BeginFrame", o.Name())
	}
	o.drawing = nil
	s.started = o.frameStart
	if s.painted != nil {
		s.painted(damage)
	}
	if o.b.paused {
		return nil
	}
	if s.prepareLater != nil {
		o.flipping = true
		return s.prepareLater(func(err error) error { return o.prepared(s, err) })
	}
	if s.prepare != nil {
		if s.syncRead {
			o.b.syncReads++
		}
		if err := s.prepare(); err != nil {
			return fmt.Errorf("drm: copy the frame of %s to its device: %w", o.Name(), err)
		}
	}
	return o.commitPresent(s)
}

func (o *Output) prepared(s *scanout, err error) error {
	o.flipping = false
	return o.presentLater(s, err)
}

func (o *Output) presentLater(s *scanout, err error) error {
	if !o.enabled || o.off || o.b.paused || !slices.Contains(o.buffers, s) {
		return err
	}
	if err == nil {
		err = o.commitPresent(s)
	}
	return o.heldFailed(err)
}

func (o *Output) heldFailed(err error) error {
	if err == nil || backend.Fatal(err) {
		return err
	}
	o.b.report(fmt.Errorf("drm: present the frame of %s it held: %w", o.Name(), err))
	return o.b.handler.OutputDamaged(o)
}

func (b *Backend) flipsPending() bool {
	return slices.ContainsFunc(b.outputs, func(o *Output) bool { return o.flipping })
}

func (b *Backend) commitHeld() error {
	if b.flipsPending() {
		return nil
	}
	var errs []error
	for _, o := range slices.Clone(b.outputs) {
		if s := o.busy; s != nil {
			o.busy = nil
			errs = append(errs, o.presentLater(s, nil))
		}
		if o.offBusy {
			o.offBusy = false
			if err := o.powerOff(); err != nil {
				o.b.report(fmt.Errorf("%w; the output stays lit until it next changes", err))
			}
		}
	}
	return errors.Join(errs...)
}

func (o *Output) commitPresent(s *scanout) error {
	fence, err := s.fence()
	if err != nil {
		return err
	}
	if o.flipping {
		o.late, o.lateFence = s, fence
		return nil
	}
	if o.kms == kmsFlip && !o.tearFrame && o.cursor != nil && o.cursor.visible {
		if wait := o.planesWait(); wait > 0 {
			o.late, o.lateFence, o.lateTimed, o.flipping = s, fence, true, true
			return o.commitLatch.Arm(wait)
		}
	}
	return o.commitFenced(s, fence)
}

func (o *Output) lateDue() error {
	if !o.lateTimed {
		return nil
	}
	o.lateTimed, o.flipping = false, false
	return o.commitLate()
}

func (o *Output) commitLate() error {
	s, fence := o.late, o.lateFence
	o.late, o.lateFence = nil, -1
	if !o.enabled || o.off || o.b.paused || !slices.Contains(o.buffers, s) {
		return closeFence(fence)
	}
	return o.heldFailed(o.commitFenced(s, fence))
}

func (o *Output) commitPipelined() error {
	if o.late == nil || o.lateTimed || o.flipping {
		return nil
	}
	return errors.Join(o.commitLate(), o.clock.Kick())
}

func (o *Output) dropLate() error {
	fence := o.lateFence
	if o.lateTimed {
		o.flipping = false
	}
	o.late, o.lateFence, o.lateTimed = nil, -1, false
	return closeFence(fence)
}

func closeFence(fd int) error {
	if fd < 0 {
		return nil
	}
	return unix.Close(fd)
}

func (o *Output) commitFenced(s *scanout, fence int) error {
	var err error
	flags := planeFlags
	if o.kms == kmsModeset {
		flags |= kdrm.AtomicAllowModeset
	}
	torn := o.tearFrame && flags == planeFlags && o.tearable(s.fb, flags|kdrm.AtomicPageFlipAsync, fence)
	if torn {
		err = o.commitFrame(flags | kdrm.AtomicPageFlipAsync)
		torn = !refused(err)
	}
	if !torn {
		err = o.submit(s, fence, flags)
	}
	busy := errors.Is(err, unix.EBUSY) && flags&kdrm.AtomicAllowModeset != 0
	if busy && o.b.flipsPending() {
		o.busy = s
		err = o.dropRestated()
		if fence >= 0 {
			err = errors.Join(err, unix.Close(fence))
		}
		return err
	}
	if busy {
		err = o.submit(s, fence, flags&^kdrm.AtomicNonblock)
	}
	restated := o.restatedFlips + len(o.tiles)
	err = errors.Join(err, o.dropRestated())
	if err != nil {
		if refused(err) {
			o.overlayRefused()
		}
		if fence >= 0 {
			err = errors.Join(err, unix.Close(fence))
		}
		return err
	}
	if err := o.dropRenderFence(); err != nil {
		return errors.Join(err, unix.Close(fence))
	}
	o.renderFence, o.renderStart = fence, s.started
	o.aimedAt = 0
	if !o.vrrSent {
		now := o.b.now()
		o.aimedAt, _ = backend.NextVblank(o.vblank(now), now, modeOf(o.mode).Interval())
	}
	o.sentOverlay()
	if flags&kdrm.AtomicAllowModeset != 0 {
		o.bpc = o.maxBPC()
	}
	o.kms, o.lit, o.flipping, o.torn, o.restate, o.extraFlips = kmsFlip, true, true, torn, false, restated
	o.queued = s
	return nil
}

func (o *Output) setDamageClips(damage render.Region) {
	if _, ok := o.primary.props["FB_DAMAGE_CLIPS"]; !ok {
		return
	}
	whole := image.Rectangle{Max: modeOf(o.mode).Size}
	if len(damage) == 0 || len(damage) == 1 && whole.In(damage[0]) {
		damage = nil
	}
	if slices.Equal(damage, o.damageRects) {
		return
	}
	var blob uint32
	if damage != nil {
		o.damageBytes = o.damageBytes[:0]
		for _, r := range damage {
			o.damageBytes = binary.NativeEndian.AppendUint32(o.damageBytes, uint32(r.Min.X)) //nolint:gosec // damage lies inside the output
			o.damageBytes = binary.NativeEndian.AppendUint32(o.damageBytes, uint32(r.Min.Y)) //nolint:gosec // as above
			o.damageBytes = binary.NativeEndian.AppendUint32(o.damageBytes, uint32(r.Max.X)) //nolint:gosec // as above
			o.damageBytes = binary.NativeEndian.AppendUint32(o.damageBytes, uint32(r.Max.Y)) //nolint:gosec // as above
		}
		var err error
		if blob, err = o.b.dev.CreateBlob(o.damageBytes); err != nil {
			o.b.report(fmt.Errorf("drm: damage clips on %s: %w; repainting the panel whole", o.Name(), err))
			blob, damage = 0, nil
		}
	}
	if o.damageBlob != 0 {
		if err := o.b.dev.DestroyBlob(o.damageBlob); err != nil {
			o.b.report(fmt.Errorf("drm: release the damage clips of %s: %w", o.Name(), err))
		}
	}
	o.damageBlob = blob
	o.damageRects = append(o.damageRects[:0], damage...)
}

func (o *Output) ScanoutFormats() gpu.ScanoutTarget {
	return gpu.ScanoutTarget{Device: o.b.number, Formats: o.primary.clients}
}

func (o *Output) ForgetScanout(key backend.ScanoutKey) error {
	o.b.forget(key)
	return nil
}

func (o *Output) Scanout(key backend.ScanoutKey, d gpu.DMABUF, release func() error, tear bool) (bool, error) {
	if !o.enabled || o.off || o.b.paused || o.kms != kmsFlip || o.drawing != nil || o.flipping || d.Size != modeOf(o.mode).Size || !slices.Contains(o.primary.clients, gpu.FormatModifier{Format: d.Format, Modifier: d.Modifier}) {
		return false, nil
	}
	o.setDamageClips(nil)
	cf, err := o.b.clientFB(key, d)
	if err != nil {
		return false, nil
	}
	s := o.takeScanout(cf, release)
	flags := planeFlags
	torn := tear && o.asyncFormat(d) && o.tearable(cf.fb, flags|kdrm.AtomicPageFlipAsync, -1)
	if torn {
		flags |= kdrm.AtomicPageFlipAsync
	} else {
		o.frame(s, -1)
		if o.verify(cf.fb, flags, true) != nil {
			o.spare = append(o.spare, s)
			return false, nil
		}
	}
	if err := o.b.dev.Commit(&o.b.req, flags, uint64(o.crtc.id)); err != nil {
		clear(o.tested)
		o.spare = append(o.spare, s)
		if refused(err) {
			return false, nil
		}
		return false, fmt.Errorf("drm: commit a client buffer on %s: %w", o.Name(), err)
	}
	o.sentOverlay()
	cf.shown++
	if o.cursor != nil && !torn {
		o.cursor.sent()
	}
	o.lit, o.flipping, o.queued, o.torn, o.extraFlips = true, true, s, torn, len(o.tiles)
	if torn {
		return true, nil
	}
	o.contentDirty, o.vrrSent = false, o.vrr
	return true, o.colorSent()
}

func (o *Output) takeScanout(cf *clientFB, release func() error) *scanout {
	var s *scanout
	if n := len(o.spare); n > 0 {
		s = o.spare[n-1]
		o.spare = o.spare[:n-1]
	} else {
		s = &scanout{}
	}
	*s = scanout{client: cf, fb: cf.fb, release: release}
	return s
}

func (o *Output) asyncFormat(d gpu.DMABUF) bool {
	return o.primary.async == nil || slices.Contains(o.primary.async, gpu.FormatModifier{Format: d.Format, Modifier: d.Modifier})
}

func (o *Output) tearable(fb uint32, flags uint32, fence int) bool {
	if !o.b.async || o.tiled() || o.front == nil || o.color.dirty() || o.contentDirty || o.vrrDirty() || o.cursor != nil && o.cursor.dirty || o.overlayNext != o.overlayShown {
		return false
	}
	key := planeKey{primary: fb, flags: flags}
	r := &o.b.req
	r.Reset()
	o.primary.set(r, "FB_ID", uint64(fb))
	if fence >= 0 {
		o.primary.set(r, "IN_FENCE_FD", uint64(fence))
		if _, ok := o.primary.props["FB_DAMAGE_CLIPS"]; ok {
			o.primary.set(r, "FB_DAMAGE_CLIPS", uint64(o.damageBlob))
		}
	}
	if ok, seen := o.tested[key]; seen {
		return ok
	}
	err := o.test(flags)
	if err == nil || refused(err) {
		o.remember(key, err == nil)
	}
	return err == nil
}

func (o *Output) TearFrame(tear bool) {
	o.tearFrame = tear
}

type planeKey struct {
	primary, cursor uint32
	overlay         overlayTest
	cursorClipped   bool
	hotspot         image.Point
	flags           uint32
}

const testedPlaneStates = 16

func (o *Output) planeKey(fb, flags uint32) planeKey {
	k := planeKey{primary: fb, flags: flags}
	if u := o.overlayNext; u != nil {
		k.overlay = overlayTest{fb: u.client.fb, dst: u.dst, yuv: u.yuv}
	}
	if c := o.cursor; c != nil && c.visible {
		k.cursor, k.cursorClipped, k.hotspot = c.pending.fb, c.clipped(), c.hotspot
	}
	return k
}

func (o *Output) verify(fb, flags uint32, keepRefusal bool) error {
	key := o.planeKey(fb, flags)
	if ok, seen := o.tested[key]; seen && o.kms == kmsFlip && !o.color.dirty() {
		if ok {
			return nil
		}
		return fmt.Errorf("drm: %s refused framebuffer %d before: %w", o.Name(), fb, unix.EINVAL)
	}
	err := o.test(flags)
	if err == nil || keepRefusal && refused(err) {
		o.remember(key, err == nil)
	}
	return err
}

func (o *Output) remember(key planeKey, ok bool) {
	if len(o.tested) >= testedPlaneStates {
		clear(o.tested)
	}
	o.tested[key] = ok
}

func (b *Backend) forgetTests(fb uint32) {
	for _, o := range b.outputs {
		maps.DeleteFunc(o.tested, func(k planeKey, _ bool) bool { return k.primary == fb || k.overlay.fb == fb })
		maps.DeleteFunc(o.overlayTested, func(k overlayTest, _ bool) bool { return k.fb == fb })
	}
}

func (o *Output) submit(s *scanout, fence int, flags uint32) error {
	o.frame(s, fence)
	err := o.verify(s.fb, flags, false)
	cursorAtFault := err != nil
	if err == nil {
		err = o.commitFrame(flags)
		cursorAtFault = refused(err) && o.cursor != nil && o.cursor.dirty
	}
	if c := o.cursor; cursorAtFault && c != nil && c.visible {
		c.visible, c.dirty = false, true
		o.frame(s, fence)
		retry := o.test(flags)
		if retry == nil {
			retry = o.commitFrame(flags)
		}
		if retry != nil {
			c.visible = true
			return err
		}
		c.refused, err = err, nil
	}
	if err != nil {
		return err
	}
	if o.cursor != nil {
		o.cursor.sent()
	}
	o.contentDirty, o.vrrSent = false, o.vrr
	return o.colorSent()
}

func (o *Output) commitFrame(flags uint32) error {
	if err := o.b.dev.Commit(&o.b.req, flags, uint64(o.crtc.id)); err != nil {
		clear(o.tested)
		return fmt.Errorf("drm: commit the frame for %s: %w", o.Name(), err)
	}
	return nil
}

func (o *Output) frame(s *scanout, fence int) {
	r := &o.b.req
	r.Reset()
	switch o.kms {
	case kmsFlip:
		o.primary.set(r, "FB_ID", uint64(s.fb))
		if _, ok := o.primary.props["FB_DAMAGE_CLIPS"]; ok {
			o.primary.set(r, "FB_DAMAGE_CLIPS", uint64(o.damageBlob))
		}
		if fence >= 0 {
			o.primary.set(r, "IN_FENCE_FD", uint64(fence))
		}
		o.flipTiles(s.fb, fence)
	case kmsModeset:
		o.addModeset(r, o.modeBlob)
		o.addSignal(r)
		o.setConnectors(r, propLinkStatus, linkStatusGood)
		if o.restate {
			o.addRestatement(r)
		}
		fallthrough
	default:
		lead := o.crtcMode(o.mode)
		o.place(o.primary, s.fb, image.Rectangle{Max: image.Pt(int(lead.HDisplay), int(lead.VDisplay))}, fence)
		o.placeTiles(s.fb, fence)
	}
	o.addOverlay()
	if c := o.cursor; c != nil && (c.dirty || o.kms != kmsFlip) {
		c.add()
	}
	o.addColor()
	o.addContentType(r)
	o.addVRR(r)
}

func (p *plane) setPosition(r *kdrm.Request, at image.Point) {
	p.set(r, "CRTC_X", uint64(int64(at.X))) //nolint:gosec // signed plane positions travel as two's complement
	p.set(r, "CRTC_Y", uint64(int64(at.Y))) //nolint:gosec // signed plane positions travel as two's complement
}

func (o *Output) place(p *plane, fb uint32, at image.Rectangle, fence int) {
	o.placeOn(p, o.crtc, fb, image.Point{}, at, fence)
}

func (o *Output) placeOn(p *plane, cr *crtc, fb uint32, src image.Point, at image.Rectangle, fence int) {
	w, h := uint64(at.Dx()), uint64(at.Dy()) //nolint:gosec // plane sizes are positive
	p.set(&o.b.req, "FB_ID", uint64(fb))
	p.set(&o.b.req, "CRTC_ID", uint64(cr.id))
	p.set(&o.b.req, "SRC_X", uint64(src.X)<<16) //nolint:gosec // a source offset is not negative
	p.set(&o.b.req, "SRC_Y", uint64(src.Y)<<16) //nolint:gosec // a source offset is not negative
	p.set(&o.b.req, "SRC_W", w<<16)
	p.set(&o.b.req, "SRC_H", h<<16)
	p.setPosition(&o.b.req, at.Min)
	p.set(&o.b.req, "CRTC_W", w)
	p.set(&o.b.req, "CRTC_H", h)
	if fence >= 0 {
		p.set(&o.b.req, "IN_FENCE_FD", uint64(fence))
	}
}

func (o *Output) test(flags uint32) error {
	test := flags&^(kdrm.AtomicNonblock|kdrm.AtomicPageFlipEvent) | kdrm.AtomicTestOnly
	if err := o.b.dev.Commit(&o.b.req, test, 0); err != nil {
		return fmt.Errorf("drm: test the frame for %s: %w", o.Name(), err)
	}
	return nil
}

func (o *Output) planesWait() time.Duration {
	if o.vrrSent {
		return 0
	}
	now := o.b.now()
	return backend.PlaneLatch(o.vblank(now), now, modeOf(o.mode).Interval()) - now
}

func (o *Output) frameWait() time.Duration {
	if o.vrrSent {
		return 0
	}
	now := o.b.now()
	return o.budget.Deadline(o.vblank(now), now, modeOf(o.mode).Interval()) - now
}

func (o *Output) vblank(now time.Duration) time.Duration {
	if o.lastFlip == 0 || now-o.lastFlip <= modeOf(o.mode).Interval() || o.unanchored || !o.lit || o.off {
		return o.lastFlip
	}
	v, err := o.b.dev.CRTCSequence(o.crtc.id)
	switch {
	case errors.Is(err, kdrm.ErrNoVblankSequence):
		o.unanchored = true
	case err != nil:
		o.unanchored = true
		o.b.report(fmt.Errorf("drm: %s extrapolates its vblanks from its last flip, reading the CRTC's last vblank failed: %w", o.Name(), err))
	case v.Active && v.Time > o.lastFlip && v.Time <= now:
		o.lastFlip = v.Time
	}
	return o.lastFlip
}

func (o *Output) dropRenderFence() error {
	if o.renderFence < 0 {
		return nil
	}
	fd := o.renderFence
	o.renderFence = -1
	return unix.Close(fd)
}

func (o *Output) timeFrame(flip time.Duration) error {
	interval := modeOf(o.mode).Interval()
	if o.aimedAt != 0 && flip > o.aimedAt+interval/2 {
		o.budget.Missed(interval)
	}
	o.aimedAt = 0
	if o.renderFence < 0 || o.untimed {
		return o.dropRenderFence()
	}
	done, ok, err := o.b.fenceTime(o.renderFence)
	if err != nil {
		o.untimed = true
		o.b.report(fmt.Errorf("drm: %s renders on the half-refresh latch, its frame fences report no time: %w", o.Name(), err))
	}
	if ok && done > o.renderStart {
		o.budget.Sample(done - o.renderStart)
	}
	return o.dropRenderFence()
}

const planeFlags = uint32(kdrm.AtomicNonblock | kdrm.AtomicPageFlipEvent)

func (o *Output) planesDue() error {
	o.planesWaiting = false
	cursor, pending := o.pendingPlanes()
	if !pending {
		return nil
	}
	return o.sendPlanes(cursor)
}

func (o *Output) flushPlanes() error {
	cursor, pending := o.pendingPlanes()
	if !pending {
		return nil
	}
	if !o.color.dirty() {
		if wait := o.planesWait(); wait > 0 {
			if o.planesWaiting {
				return nil
			}
			o.planesWaiting = true
			return o.planesLatch.Arm(wait)
		}
	}
	return o.sendPlanes(cursor)
}

func (o *Output) pendingPlanes() (cursor, pending bool) {
	c := o.cursor
	cursor = c != nil && c.dirty
	if cursor && c.settled() {
		c.dirty, cursor = false, false
	}
	if !cursor && !o.color.dirty() || !o.lit || o.kms != kmsFlip || o.off || o.b.paused || o.flipping || o.frameCarriesPlanes() {
		return cursor, false
	}
	return cursor, true
}

func (o *Output) sendPlanes(cursor bool) error {
	c := o.cursor
	err := o.commitPlanes(cursor)
	if cursor && err != nil && o.cursorRefused(err) {
		c.refused, c.visible, cursor = fmt.Errorf("drm: the cursor plane of %s refuses the cursor at %v: %w", o.Name(), c.at, err), false, false
		if !o.color.dirty() {
			return nil
		}
		err = o.commitPlanes(false)
	}
	if err != nil {
		return o.planesFailed(fmt.Errorf("drm: commit the cursor and colour of %s: %w", o.Name(), err))
	}
	o.planesFailing = false
	o.flipping, o.extraFlips = true, len(o.tiles)
	if cursor {
		c.sent()
	}
	return o.colorSent()
}

func (o *Output) frameCarriesPlanes() bool {
	switch {
	case o.framing || o.clock.Queued():
		return true
	case !o.clock.Waiting():
		return false
	}
	now := o.b.now()
	next, _ := backend.NextVblank(o.vblank(now), now, modeOf(o.mode).Interval())
	return o.frameDue < next
}

func (o *Output) commitPlanes(cursor bool) error {
	r := &o.b.req
	r.Reset()
	if cursor {
		o.cursor.add()
	}
	o.addColor()
	return o.b.dev.Commit(r, planeFlags, uint64(o.crtc.id))
}

func (o *Output) cursorRefused(err error) bool {
	if !refused(err) {
		return false
	}
	if !o.color.dirty() {
		return true
	}
	o.b.req.Reset()
	o.cursor.add()
	return o.test(planeFlags) != nil
}

func refused(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ERANGE)
}

func (o *Output) planesFailed(err error) error {
	if backend.Fatal(err) {
		return err
	}
	if !o.planesFailing {
		o.planesFailing = true
		o.b.report(fmt.Errorf("%w; keeping the planes as they are until a commit takes", err))
	}
	return nil
}

func (o *Output) flipped(e kdrm.FlipEvent) error {
	if !o.flipping {
		return nil
	}
	if o.extraFlips > 0 {
		o.extraFlips--
		return nil
	}
	o.flipping = false
	o.lastFlip = time.Duration(e.Sec)*time.Second + time.Duration(e.Usec)*time.Microsecond
	overlayFreed := o.overlayFlipped()
	timing := o.timeFrame(o.lastFlip)
	if c := o.cursor; c != nil && c.queued != nil {
		c.front, c.queued = c.queued, nil
	}
	if o.queued == nil {
		defer o.powerOffAfterFlip()
		var presented error
		if overlayFreed.changed {
			presented = o.b.handler.Presented(o, o.presentation(e))
		}
		presented = errors.Join(overlayFreed.err, presented)
		return errors.Join(presented, timing, o.clock.Kick(), o.flushPlanes())
	}
	var freed error
	if old := o.front; old != nil && old.client != nil {
		freed = old.free(o.b.dev)
		o.spare = append(o.spare, old)
	}
	o.front, o.queued = o.queued, nil
	o.presented++
	o.front.presented = o.presented
	presented := o.b.handler.Presented(o, o.presentation(e))
	defer o.powerOffAfterFlip()
	return errors.Join(overlayFreed.err, freed, presented, timing, o.clock.Kick(), o.flushPlanes())
}

func (o *Output) presentation(e kdrm.FlipEvent) backend.Presentation {
	return backend.Presentation{Time: o.lastFlip, Refresh: modeOf(o.mode).Interval(), Seq: uint64(e.Sequence), Torn: o.torn, Hardware: e.Sequence != 0, Variable: o.vrrSent}
}

func (o *Output) dropScanout(s *scanout) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.free(o.b.dev)
}

func (o *Output) dropScanouts() error {
	err := errors.Join(o.dropScanout(o.front), o.dropScanout(o.queued), o.dropOverlays())
	o.front, o.queued = nil, nil
	return err
}

func (o *Output) freeBuffers() error {
	err := errors.Join(o.dropLate(), o.dropScanouts(), o.free(o.buffers))
	o.buffers, o.drawing, o.queued, o.front, o.busy = nil, nil, nil, nil, nil
	return err
}

func (o *Output) leaveOnScreen() error {
	if o.kms == kmsInherited && o.enabled && !o.off && !o.b.paused {
		o.enabled, o.lit = false, false
		return o.freeBuffers()
	}
	if !o.enabled || !o.lit || o.off || o.b.paused || o.front == nil {
		return o.disable()
	}
	shown := []*scanout{o.front, o.queued}
	var errs []error
	for _, s := range shown {
		if s != nil {
			errs = append(errs, s.keepOnScreen(o.b.dev))
		}
	}
	for _, s := range o.buffers {
		if !slices.Contains(shown, s) {
			errs = append(errs, s.free(o.b.dev))
		}
	}
	o.buffers, o.drawing, o.queued, o.front = nil, nil, nil, nil
	o.enabled, o.lit, o.flipping = false, false, false
	o.clock.Cancel()
	return errors.Join(errs...)
}

func (o *Output) release() error {
	o.setDamageClips(nil)
	err := errors.Join(o.freeBuffers(), o.releaseColor(), o.releaseSignal(), o.dropRenderFence(), o.dropRestated(), o.releaseTiles())
	if o.modeBlob != 0 {
		err = errors.Join(err, o.b.dev.DestroyBlob(o.modeBlob))
		o.modeBlob = 0
	}
	o.unbind()
	if o.latch != nil {
		err = errors.Join(err, o.latch.Remove())
		o.latch = nil
	}
	if o.pacer != nil {
		err = errors.Join(err, o.pacer.Remove())
		o.pacer = nil
	}
	if o.planesLatch != nil {
		err = errors.Join(err, o.planesLatch.Remove())
		o.planesLatch = nil
	}
	if o.commitLatch != nil {
		err = errors.Join(err, o.commitLatch.Remove())
		o.commitLatch = nil
	}
	if o.cursor != nil {
		err = errors.Join(err, o.cursor.release())
		o.cursor = nil
	}
	return err
}

func (b *Backend) Park() error {
	if b.src == nil {
		return nil
	}
	return b.src.Modify(0)
}

func (b *Backend) Unpark() error {
	if b.src == nil {
		return nil
	}
	return b.src.Modify(loop.Readable)
}

func (b *Backend) Flipping() string {
	for _, o := range b.outputs {
		if o.flipping {
			return fmt.Sprintf("output %s has a flip on its way", o.Name())
		}
	}
	return ""
}
