package shell

import (
	"fmt"

	sxdg "github.com/stubbedev/lys-wm/internal/protocol/server/xdgshell"
	sxtt "github.com/stubbedev/lys-wm/internal/protocol/server/xdgtopleveltag"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const tagVersion = 1

func bindTags(c *server.Client, id wire.ObjectID, version uint32) error {
	_, err := sxtt.NewXdgToplevelTagManagerV1(c, id, version, tagManager{})
	return err
}

type tagManager struct{}

func (tagManager) Destroy(*sxtt.XdgToplevelTagManagerV1) error { return nil }

func taggedToplevel(top *sxdg.XdgToplevel) (*Toplevel, error) {
	t, ok := top.Handler().(*Toplevel)
	if !ok {
		return nil, fmt.Errorf("shell: xdg_toplevel@%d has handler %T", top.Base().ID(), top.Handler())
	}
	return t, nil
}

func (tagManager) SetToplevelTag(_ *sxtt.XdgToplevelTagManagerV1, top *sxdg.XdgToplevel, tag string) error {
	t, err := taggedToplevel(top)
	if err != nil || t.tag == tag {
		return err
	}
	t.tag = tag
	return t.xs.base.shell.listener.ToplevelChanged(t, ChangedTag)
}

func (tagManager) SetToplevelDescription(_ *sxtt.XdgToplevelTagManagerV1, top *sxdg.XdgToplevel, description string) error {
	t, err := taggedToplevel(top)
	if err != nil {
		return err
	}
	t.description = description
	return nil
}
