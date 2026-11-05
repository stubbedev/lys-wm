package shell

import (
	"fmt"
	"slices"
	"testing"

	cxtt "github.com/stubbedev/lys-wm/internal/protocol/client/xdgtopleveltag"
)

func (c *client) tagManager() *cxtt.XdgToplevelTagManagerV1 {
	c.t.Helper()
	globals := registry{}
	reg, err := c.d.WlDisplay().GetRegistry(globals)
	must(c.t, err)
	c.roundtrip()
	name, ok := globals[cxtt.XdgToplevelTagManagerV1Interface.Name]
	if !ok {
		c.t.Fatalf("no %s global", cxtt.XdgToplevelTagManagerV1Interface.Name)
	}
	m, err := cxtt.NewXdgToplevelTagManagerV1(c.d, tagVersion)
	must(c.t, err)
	must(c.t, reg.Bind(name, m))
	return m
}

func TestAToplevelKeepsTheTagAndDescriptionItsClientSets(t *testing.T) {
	e := setup(t)
	c := e.connect(6)
	m := c.tagManager()
	w := c.window()
	must(t, w.top.SetAppID("app"), m.SetToplevelTag(w.top, "settings"), m.SetToplevelDescription(w.top, "Einstellungen"), w.surf.Commit())
	c.roundtrip()
	must(t, m.SetToplevelTag(w.top, "settings"))
	c.roundtrip()
	e.on(func() {
		if tl := e.rec.toplevels[0]; tl.Tag() != "settings" || tl.Description() != "Einstellungen" {
			t.Errorf("tag %q, description %q", tl.Tag(), tl.Description())
		}
	})
	tagged := fmt.Sprintf(`changed %d "" "app"`, ChangedTag)
	if got := e.log(); !slices.Equal(got, []string{`changed 2 "" "app"`, tagged, "initial"}) {
		t.Fatalf("events %v: want the tag reported once, and not again when set to itself", got)
	}
	must(t, m.SetToplevelTag(w.top, "main window"))
	c.roundtrip()
	if got := e.log(); got[len(got)-1] != tagged {
		t.Fatalf("events %v: a new tag was not reported", got)
	}
}
