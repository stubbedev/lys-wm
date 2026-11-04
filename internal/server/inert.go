package server

import (
	"fmt"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/wire"
)

type inertObject struct {
	object wire.Object
}

func (o *inertObject) Base() *wire.Object {
	return &o.object
}

func (c *Client) addInert(id wire.ObjectID, iface *wire.Interface, version uint32) error {
	object, err := wire.NewObject(c, id, version, iface)
	if err != nil {
		return err
	}
	o := &inertObject{object}
	if err := c.Add(o); err != nil {
		return err
	}
	c.inerts++
	OnDestroy(o, func() error {
		c.inerts--
		return nil
	})
	return nil
}

func (c *Client) inertBound(name string, version uint32, id wire.ObjectID) error {
	i := slices.IndexFunc(c.display.globals, func(g *Global) bool { return g.iface.Name == name })
	if i >= 0 {
		return c.addInert(id, c.display.globals[i].iface, version)
	}
	iface, ok := inertInterfaces[name]
	if !ok {
		return fmt.Errorf("server: this lys-wm has no %s, which a client holds", name)
	}
	return c.addInert(id, iface, version)
}

func (o *inertObject) Dispatch(m *wire.Message) error {
	return clientOf(o).ignore(o, m)
}

func (c *Client) namesInert(r wire.Resource, m *wire.Message) bool {
	iface := r.Base().Interface()
	if int(m.Opcode) >= len(iface.Requests) {
		return false
	}
	data, found := m.Raw(), false
	if err := wire.ArgWalk(data, iface.Requests[m.Opcode].Args, func(a wire.Arg, off int) {
		if a.Type != wire.ArgObject {
			return
		}
		if target, ok := c.objects[wire.ObjectAt(data, off)]; ok {
			_, inert := target.(*inertObject)
			found = found || inert
		}
	}); err != nil {
		return false
	}
	return found
}

func (c *Client) ignore(r wire.Resource, m *wire.Message) error {
	iface := r.Base().Interface()
	if int(m.Opcode) >= len(iface.Requests) {
		return m.Finish()
	}
	spec, data := iface.Requests[m.Opcode], m.Raw()
	var children []wire.ObjectID
	var childIfaces []string
	var childName string
	nfds := 0
	if err := wire.ArgWalk(data, spec.Args, func(a wire.Arg, off int) {
		switch a.Type {
		case wire.ArgFD:
			nfds++
		case wire.ArgString:
			if a.Name == "interface" {
				childName = wire.StringAt(data, off)
			}
		case wire.ArgNewID:
			name := a.Interface
			if name == "" {
				name = childName
			}
			children, childIfaces = append(children, wire.ObjectAt(data, off)), append(childIfaces, name)
		}
	}); err != nil {
		return err
	}
	for range nfds {
		if fd := m.FD(); fd >= 0 {
			if err := unix.Close(fd); err != nil {
				return fmt.Errorf("server: close a fd sent to an inert %s: %w", iface.Name, err)
			}
		}
	}
	m.Skip()
	if err := m.Finish(); err != nil {
		return err
	}
	for i, id := range children {
		child, ok := inertInterfaces[childIfaces[i]]
		if !ok {
			return fmt.Errorf("server: a request on an inert %s creates %s, which cannot be inert", iface.Name, childIfaces[i])
		}
		if err := c.addInert(id, child, r.Base().Version()); err != nil {
			return err
		}
	}
	if _, isInert := r.(*inertObject); isInert && spec.Destructor {
		return c.Remove(r.Base().ID())
	}
	return nil
}
