// Package shell implements xdg-shell: the window roles clients request and
// the configure handshake the window manager drives.
package shell

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"time"

	"github.com/stubbedev/lys-wm/internal/loop"
	sel "github.com/stubbedev/lys-wm/internal/protocol/server/extsessionlock"
	skd "github.com/stubbedev/lys-wm/internal/protocol/server/kdeserverdecoration"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	swlr "github.com/stubbedev/lys-wm/internal/protocol/server/wlrlayershell"
	sxa "github.com/stubbedev/lys-wm/internal/protocol/server/xdgactivation"
	sxd "github.com/stubbedev/lys-wm/internal/protocol/server/xdgdecoration"
	sxdd "github.com/stubbedev/lys-wm/internal/protocol/server/xdgdialog"
	sxf1 "github.com/stubbedev/lys-wm/internal/protocol/server/xdgforeignv1"
	sxf2 "github.com/stubbedev/lys-wm/internal/protocol/server/xdgforeignv2"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	sxti "github.com/stubbedev/lys-wm/internal/protocol/server/xdgtoplevelicon"
	sxtt "github.com/stubbedev/lys-wm/internal/protocol/server/xdgtopleveltag"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	wmBaseVersion = 7
	xdgRole       = "xdg_surface"
	PingTimeout   = 5 * time.Second
)

type Request interface {
	isRequest()
}

type (
	SetFullscreen struct{ On bool }
	SetMaximized  struct{ On bool }
	SetMinimized  struct{}
	Move          struct{ Serial uint32 }
	Resize        struct {
		Serial uint32
		Edges  sxdg.XdgToplevelResizeEdge
	}
	ShowWindowMenu struct {
		Serial uint32
		At     image.Point
	}
)

func (SetFullscreen) isRequest()  {}
func (SetMaximized) isRequest()   {}
func (SetMinimized) isRequest()   {}
func (Move) isRequest()           {}
func (Resize) isRequest()         {}
func (ShowWindowMenu) isRequest() {}

type Change uint8

const (
	ChangedTitle Change = iota + 1
	ChangedAppID
	ChangedParent
	ChangedModal
	ChangedIcon
	ChangedSizeLimits
	ChangedTag
)

type Listener interface {
	ToplevelInitialCommit(t *Toplevel) error
	ToplevelMapped(t *Toplevel) error
	ToplevelUnmapped(t *Toplevel) error
	ToplevelChanged(t *Toplevel, c Change) error
	ToplevelRequest(t *Toplevel, r Request) error
	ClientResponsive(c *server.Client, responsive bool) error
	PopupConstraint(root PopupRoot) image.Rectangle
	LayerCommitted(l *LayerSurface) error
	LayerUnmapped(l *LayerSurface) error
	LayerDestroyed(l *LayerSurface) error
	PopupMapped(p *Popup) error
	PopupUnmapped(p *Popup) error
	UserActive(c *server.Client, serial uint32) bool
	MintActivationToken(c *server.Client, serial uint32) (string, error)
	GrabChanged() error
	LockChanged() error
	LockReleasing() error
	LockOutputSize(output *sw.WlOutput) (image.Point, bool)
}

type Shell struct {
	display     *server.Display
	loop        *loop.Loop
	listener    Listener
	subRoles    map[*surface.Surface]string
	pingTimeout time.Duration
	grab        []*Popup
	lock        *SessionLock
	locked      bool
	exports     map[string]*export
	toplevels   []*Toplevel
	iconSerial  uint64
}

func New(d *server.Display, l *loop.Loop, listener Listener) (*Shell, error) {
	sh := &Shell{display: d, loop: l, listener: listener, subRoles: map[*surface.Surface]string{}, pingTimeout: PingTimeout, exports: map[string]*export{}}
	if _, err := d.AddGlobal(sxdg.XdgWmBaseInterface, wmBaseVersion, sh.bind); err != nil {
		return nil, err
	}
	d.OnPrepareHandover(sh.markRetiredRoles)
	if _, err := d.AddGlobal(swlr.ZwlrLayerShellV1Interface, layerShellVersion, sh.bindLayerShell); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(sxd.ZxdgDecorationManagerV1Interface, decorationVersion, sh.bindDecoration); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(sxa.XdgActivationV1Interface, activationVersion, sh.bindActivation); err != nil {
		return nil, err
	}
	if _, err := d.AddGlobal(sel.ExtSessionLockManagerV1Interface, lockVersion, sh.bindLock); err != nil {
		return nil, err
	}
	for _, g := range []struct {
		iface   *wire.Interface
		version uint32
		bind    server.BindFunc
	}{
		{sxf1.ZxdgExporterV1Interface, foreignVersion, sh.bindExporterV1},
		{sxf1.ZxdgImporterV1Interface, foreignVersion, sh.bindImporterV1},
		{sxf2.ZxdgExporterV2Interface, foreignVersion, sh.bindExporterV2},
		{sxf2.ZxdgImporterV2Interface, foreignVersion, sh.bindImporterV2},
		{sxdd.XdgWmDialogV1Interface, dialogVersion, sh.bindDialog},
		{sxti.XdgToplevelIconManagerV1Interface, iconVersion, sh.bindIcons},
		{sxtt.XdgToplevelTagManagerV1Interface, tagVersion, bindTags},
		{skd.OrgKdeKwinServerDecorationManagerInterface, kdeDecorationVersion, bindKDEDecoration},
	} {
		if _, err := d.AddGlobal(g.iface, g.version, g.bind); err != nil {
			return nil, err
		}
	}
	return sh, nil
}

type wmBase struct {
	shell      *Shell
	client     *server.Client
	resource   *sxdg.XdgWmBase
	surfaces   []*XdgSurface
	ping       uint32
	timer      *loop.Timer
	responsive bool
	destroyed  bool
}

func (sh *Shell) bind(c *server.Client, id wire.ObjectID, version uint32) error {
	b := &wmBase{shell: sh, client: c, responsive: true}
	res, err := sxdg.NewXdgWmBase(c, id, version, b)
	if err != nil {
		return err
	}
	b.resource = res
	server.OnDestroy(res, func() error {
		b.destroyed = true
		if b.timer == nil {
			return nil
		}
		return b.timer.Remove()
	})
	return nil
}

func (b *wmBase) Destroy(obj *sxdg.XdgWmBase) error {
	if len(b.surfaces) > 0 {
		return obj.ProtocolError(sxdg.XdgWmBaseErrorDefunctSurfaces, "%d xdg_surfaces still exist", len(b.surfaces))
	}
	return nil
}

func (b *wmBase) Pong(_ *sxdg.XdgWmBase, serial uint32) error {
	if serial != b.ping || b.ping == 0 {
		return nil
	}
	b.ping = 0
	if err := b.timer.Disarm(); err != nil {
		return err
	}
	if !b.responsive {
		b.responsive = true
		return b.shell.listener.ClientResponsive(b.client, true)
	}
	return nil
}

func (b *wmBase) Ping() error {
	if b.destroyed || b.ping != 0 {
		return nil
	}
	if b.timer == nil {
		var err error
		if b.timer, err = b.shell.loop.AddTimer(b.timeout); err != nil {
			return err
		}
	}
	b.ping = b.shell.display.NextSerial()
	if err := b.resource.SendPing(b.ping); err != nil {
		return err
	}
	return b.timer.Arm(b.shell.pingTimeout)
}

func (b *wmBase) timeout() error {
	if b.ping == 0 || !b.responsive {
		return nil
	}
	b.responsive = false
	return b.shell.listener.ClientResponsive(b.client, false)
}

func (b *wmBase) GetXdgSurface(obj *sxdg.XdgWmBase, res *sxdg.XdgSurface, wl *sw.WlSurface) (sxdg.XdgSurfaceHandler, error) {
	surf, err := surface.From(wl)
	if err != nil {
		return nil, err
	}
	if surf.HasBuffer() {
		return nil, obj.ProtocolError(sxdg.XdgWmBaseErrorInvalidSurfaceState, "the wl_surface already has a buffer")
	}
	xs := &XdgSurface{base: b, resource: res, surface: surf}
	if err := surf.SetRole(xs); err != nil {
		return nil, obj.ProtocolError(sxdg.XdgWmBaseErrorRole, "%v", err)
	}
	b.surfaces = append(b.surfaces, xs)
	forget := func() error {
		b.surfaces = slices.DeleteFunc(b.surfaces, func(x *XdgSurface) bool { return x == xs })
		surf.ClearRole(xs)
		return nil
	}
	server.OnDestroy(res, forget)
	server.OnDestroy(wl, func() error {
		delete(b.shell.subRoles, surf)
		return nil
	})
	return xs, nil
}

type queued[T any] struct {
	serial uint32
	state  T
}

type configures[T any] struct {
	pending []queued[T]
	acked   T
	sent    T
	latest  uint32
	isAcked bool
	isSent  bool
}

func (q *configures[T]) push(serial uint32, state T) {
	q.pending = append(q.pending, queued[T]{serial, state})
	q.sent, q.isSent, q.latest = state, true, serial
}

func (q *configures[T]) waiting(serial uint32) bool {
	return slices.ContainsFunc(q.pending, func(c queued[T]) bool { return c.serial == serial })
}

func (q *configures[T]) last() (T, bool) {
	return q.sent, q.isSent
}

func (q *configures[T]) ack(serial uint32) bool {
	i := slices.IndexFunc(q.pending, func(c queued[T]) bool { return c.serial == serial })
	if i < 0 {
		return false
	}
	q.acked, q.isAcked = q.pending[i].state, true
	q.pending = slices.Delete(q.pending, 0, i+1)
	return true
}

func (q *configures[T]) take() (T, bool) {
	if !q.isAcked {
		var zero T
		return zero, false
	}
	var zero T
	state := q.acked
	q.acked, q.isAcked = zero, false
	return state, true
}

func (q *configures[T]) reset() {
	var zero T
	q.pending, q.acked, q.sent, q.isAcked, q.isSent = q.pending[:0], zero, zero, false, false
}

type roleObject interface {
	ack(serial uint32) bool
	ackAny()
	applyPending() error
	initialCommit() error
	mapped() error
	unmapped() error
	inert() bool
	placedAt() image.Point
}

type XdgSurface struct {
	base     *wmBase
	resource *sxdg.XdgSurface
	surface  *surface.Surface
	role     roleObject
	exports  []*export
	popupList
	life            roleLife
	geometry        image.Rectangle
	pendingGeometry image.Rectangle
	geometrySet     bool
}

func (xs *XdgSurface) RoleName() string {
	return xdgRole
}

func (xs *XdgSurface) Destroy(obj *sxdg.XdgSurface) error {
	if xs.role != nil {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorDefunctRoleObject, "the role object must be destroyed first")
	}
	return nil
}

func (xs *XdgSurface) sendConfigure() (uint32, error) {
	serial := xs.base.shell.display.NextSerial()
	return serial, xs.resource.SendConfigure(serial)
}

func (xs *XdgSurface) setSubRole(obj *sxdg.XdgSurface, role string) error {
	sh := xs.base.shell
	if xs.role != nil {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorAlreadyConstructed, "the xdg_surface already has a role object")
	}
	if prev, ok := sh.subRoles[xs.surface]; ok && prev != role {
		return xs.base.resource.ProtocolError(sxdg.XdgWmBaseErrorRole, "the wl_surface was an %s and cannot become an %s", prev, role)
	}
	sh.subRoles[xs.surface] = role
	return nil
}

func (xs *XdgSurface) GetToplevel(obj *sxdg.XdgSurface, res *sxdg.XdgToplevel) (sxdg.XdgToplevelHandler, error) {
	if err := xs.setSubRole(obj, "xdg_toplevel"); err != nil {
		return nil, err
	}
	t := &Toplevel{xs: xs, resource: res}
	xs.role = t
	xs.base.shell.toplevels = append(xs.base.shell.toplevels, t)
	server.OnDestroy(res, t.destroy)
	if res.Base().Version() >= 5 {
		caps := binary.NativeEndian.AppendUint32(nil, uint32(sxdg.XdgToplevelWmCapabilitiesFullscreen))
		caps = binary.NativeEndian.AppendUint32(caps, uint32(sxdg.XdgToplevelWmCapabilitiesMaximize))
		caps = binary.NativeEndian.AppendUint32(caps, uint32(sxdg.XdgToplevelWmCapabilitiesMinimize))
		if err := res.SendWmCapabilities(caps); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (xs *XdgSurface) SetWindowGeometry(obj *sxdg.XdgSurface, x, y, w, h int32) error {
	if w <= 0 || h <= 0 {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorInvalidSize, "window geometry %dx%d is not positive", w, h)
	}
	xs.pendingGeometry = image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h))
	xs.geometrySet = true
	return nil
}

func (xs *XdgSurface) AckConfigure(obj *sxdg.XdgSurface, serial uint32) error {
	if xs.role == nil {
		return obj.ProtocolError(sxdg.XdgSurfaceErrorNotConstructed, "the xdg_surface has no role object")
	}
	if xs.role.inert() {
		return nil
	}
	if xs.surface.Client().Restoring() {
		xs.role.ackAny()
		xs.life.acked()
		return nil
	}
	if !xs.role.ack(serial) {
		if xs.base.shell.display.StaleAck(serial) {
			return nil
		}
		return obj.ProtocolError(sxdg.XdgSurfaceErrorInvalidSerial, "serial %d was never sent or is already acked", serial)
	}
	xs.life.acked()
	return nil
}

func (xs *XdgSurface) Geometry() image.Rectangle {
	return xs.geometry
}

func (xs *XdgSurface) Placement() image.Rectangle {
	if xs.role == nil {
		return xs.geometry
	}
	return xs.geometry.Sub(xs.role.placedAt())
}

func (xs *XdgSurface) Committed(s *surface.Surface) error {
	r := xs.role
	if r == nil {
		return xs.resource.ProtocolError(sxdg.XdgSurfaceErrorNotConstructed, "the xdg_surface has no role object")
	}
	if r.inert() {
		return nil
	}
	if xs.geometrySet {
		xs.geometry = xs.pendingGeometry
	}
	if err := r.applyPending(); err != nil {
		return err
	}
	step, configured := xs.life.commit(s.Buffer() != nil)
	if !configured {
		return xs.resource.ProtocolError(sxdg.XdgSurfaceErrorUnconfiguredBuffer, "a buffer was attached before the first configure was acked")
	}
	if !xs.geometrySet {
		xs.geometry = s.TreeBounds()
	}
	switch step {
	case stepInitial:
		return r.initialCommit()
	case stepUnmap:
		return xs.unmapped()
	case stepMap:
		return r.mapped()
	}
	return nil
}

func (xs *XdgSurface) unmap() error {
	xs.life.reset()
	return xs.unmapped()
}

func (xs *XdgSurface) unmapped() error {
	return errors.Join(xs.dismissPopups(), xs.role.unmapped())
}

func (xs *XdgSurface) HandoverPrelude(st *server.Stream, res *sw.WlSurface) (bool, error) {
	if xs.role == nil || xs.role.inert() || !xs.life.initialSent() {
		return false, nil
	}
	if _, popup := xs.role.(*Popup); popup {
		return false, nil
	}
	return true, emitInitial(st, res, xs.resource.Base().ID(), sxdg.XdgSurfaceInterface)
}

func emitInitial(st *server.Stream, res *sw.WlSurface, role wire.ObjectID, iface *wire.Interface) error {
	e, err := st.Request(res.Base().ID(), sw.WlSurfaceInterface, "commit")
	if err != nil {
		return err
	}
	if err := e.End(); err != nil {
		return err
	}
	e, err = st.Request(role, iface, "ack_configure")
	if err != nil {
		return err
	}
	e.Uint(0)
	return e.End()
}

func (sh *Shell) markRetiredRoles(c *server.Client, st *server.Stream) error {
	for _, r := range c.Objects() {
		res, ok := r.(*sxdg.XdgSurface)
		if !ok {
			continue
		}
		xs, ok := res.Handler().(*XdgSurface)
		if !ok {
			return fmt.Errorf("shell: xdg_surface@%d has handler %T", res.Base().ID(), res.Handler())
		}
		if _, popup := xs.role.(*Popup); popup {
			st.MarkInert(res)
		}
	}
	return nil
}

func (q *configures[T]) ackAny() {
	if n := len(q.pending); n > 0 {
		q.acked, q.isAcked = q.pending[n-1].state, true
		q.pending = q.pending[:0]
		return
	}
	q.acked, q.isAcked = q.sent, true
}
