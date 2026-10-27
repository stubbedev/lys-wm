package shell

import (
	"fmt"

	skd "github.com/stubbedev/lys-wm/internal/protocol/server/kdeserverdecoration"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	sxd "github.com/stubbedev/lys-wm/internal/protocol/server/xdgdecoration"
	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const (
	decorationVersion    = 1
	kdeDecorationVersion = 1
)

func (sh *Shell) bindDecoration(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxd.NewZxdgDecorationManagerV1(c, id, version, decorationManager{})
	return err
}

type decorationManager struct{}

func (decorationManager) Destroy(*sxd.ZxdgDecorationManagerV1) error {
	return nil
}

func (decorationManager) GetToplevelDecoration(_ *sxd.ZxdgDecorationManagerV1, res *sxd.ZxdgToplevelDecorationV1, top *sxdg.XdgToplevel) (sxd.ZxdgToplevelDecorationV1Handler, error) {
	t, ok := top.Handler().(*Toplevel)
	if !ok {
		return nil, fmt.Errorf("shell: xdg_toplevel@%d has handler %T", top.Base().ID(), top.Handler())
	}
	switch {
	case t.decoration != nil:
		return nil, res.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorAlreadyConstructed, "the xdg_toplevel already has a decoration object")
	case t.xs.surface.HasBuffer():
		return nil, res.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorUnconfiguredBuffer, "the xdg_toplevel already has a buffer")
	}
	t.decoration = res
	server.OnDestroy(res, func() error {
		t.decoration = nil
		return nil
	})
	d := decoration{t}
	return d, d.configure()
}

type decoration struct{ t *Toplevel }

func (d decoration) configure() error {
	t := d.t
	if err := t.decoration.SendConfigure(sxd.ZxdgToplevelDecorationV1ModeServerSide); err != nil {
		return err
	}
	if !t.xs.life.initialSent() {
		return nil
	}
	_, err := t.Configure(t.Requested())
	return err
}

func (decoration) Destroy(*sxd.ZxdgToplevelDecorationV1) error {
	return nil
}

func (d decoration) SetMode(obj *sxd.ZxdgToplevelDecorationV1, mode sxd.ZxdgToplevelDecorationV1Mode) error {
	if mode != sxd.ZxdgToplevelDecorationV1ModeClientSide && mode != sxd.ZxdgToplevelDecorationV1ModeServerSide {
		return obj.ProtocolError(sxd.ZxdgToplevelDecorationV1ErrorInvalidMode, "mode %d", mode)
	}
	return d.configure()
}

func (d decoration) UnsetMode(*sxd.ZxdgToplevelDecorationV1) error {
	return d.configure()
}

func bindKDEDecoration(c *server.Client, id wire.ObjectID, version uint32) error {
	res, err := skd.NewOrgKdeKwinServerDecorationManager(c, id, version, kdeDecorationManager{})
	if err != nil {
		return err
	}
	return res.SendDefaultMode(uint32(skd.OrgKdeKwinServerDecorationManagerModeServer))
}

type kdeDecorationManager struct{}

func (kdeDecorationManager) Create(_ *skd.OrgKdeKwinServerDecorationManager, res *skd.OrgKdeKwinServerDecoration, _ *sw.WlSurface) (skd.OrgKdeKwinServerDecorationHandler, error) {
	return kdeDecoration{}, res.SendMode(uint32(skd.OrgKdeKwinServerDecorationModeServer))
}

type kdeDecoration struct{}

func (kdeDecoration) Release(*skd.OrgKdeKwinServerDecoration) error { return nil }

func (kdeDecoration) RequestMode(obj *skd.OrgKdeKwinServerDecoration, _ uint32) error {
	return obj.SendMode(uint32(skd.OrgKdeKwinServerDecorationModeServer))
}
