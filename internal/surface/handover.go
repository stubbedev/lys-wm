package surface

import (
	"errors"
	"fmt"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/region"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/wire"
)

type HandoverRole interface {
	HandoverPrelude(s *server.Stream, res *sw.WlSurface) (commit bool, err error)
}

func (m *Manager) surfacesOf(c *server.Client) ([]*Surface, error) {
	var out []*Surface
	for _, r := range c.Objects() {
		res, ok := r.(*sw.WlSurface)
		if !ok {
			continue
		}
		s, err := From(res)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func request(st *server.Stream, id wire.ObjectID, name string, write func(*wire.Encoder)) error {
	e, err := st.Request(id, sw.WlSurfaceInterface, name)
	if err != nil {
		return err
	}
	if write != nil {
		write(e)
	}
	return e.End()
}

func (m *Manager) emit(c *server.Client, st *server.Stream) error {
	surfaces, err := m.surfacesOf(c)
	if err != nil {
		return err
	}
	done := map[*Surface]bool{}
	var visit func(s *Surface) error
	visit = func(s *Surface) error {
		if done[s] {
			return nil
		}
		done[s] = true
		for _, child := range s.pendingOrder {
			if child != s {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return s.emitCurrent(st)
	}
	for _, s := range surfaces {
		if s.sub == nil || s.sub.parent == nil {
			if err := visit(s); err != nil {
				return err
			}
		}
	}
	for _, s := range surfaces {
		if err := visit(s); err != nil {
			return err
		}
	}
	for _, s := range surfaces {
		if err := s.emitUncommitted(st); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) emitPlacement(st *server.Stream) error {
	for i, child := range s.pendingOrder {
		if child == s {
			continue
		}
		neighbour, op := s.pendingOrder[max(i-1, 0)], "place_above"
		if i == 0 {
			neighbour, op = s.pendingOrder[1], "place_below"
		}
		e, err := st.Request(child.sub.resource.Base().ID(), sw.WlSubsurfaceInterface, op)
		if err != nil {
			return err
		}
		e.Object(neighbour.resource.Base().ID())
		if err := e.End(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Surface) emitCurrent(st *server.Stream) error {
	id := s.resource.Base().ID()
	if role, ok := s.role.(HandoverRole); ok {
		commit, err := role.HandoverPrelude(st, s.resource)
		if err != nil || !commit {
			return err
		}
	}
	if err := s.emitPlacement(st); err != nil {
		return err
	}
	if b := s.current.buffer; b != nil {
		buffer, err := aliveBuffer(b)
		if err != nil {
			return fmt.Errorf("wl_surface@%d: %w", id, err)
		}
		if err := request(st, id, "attach", func(e *wire.Encoder) { e.Object(buffer); e.Int(0); e.Int(0) }); err != nil {
			return err
		}
		size := b.Size()
		if err := request(st, id, "damage_buffer", func(e *wire.Encoder) {
			e.Int(0)
			e.Int(0)
			e.Int(int32(size.X)) //nolint:gosec // a buffer's size came from int32 arguments
			e.Int(int32(size.Y)) //nolint:gosec // a buffer's size came from int32 arguments
		}); err != nil {
			return err
		}
	}
	if err := s.emitState(st, &s.current); err != nil {
		return err
	}
	for _, h := range s.hooks {
		if h.Handover != nil {
			if err := h.Handover(st); err != nil {
				return err
			}
		}
	}
	return request(st, id, "commit", nil)
}

func (s *Surface) emitState(st *server.Stream, state *state) error {
	id := s.resource.Base().ID()
	if state.scale > 1 || state.set&fieldScale != 0 {
		if err := request(st, id, "set_buffer_scale", func(e *wire.Encoder) { e.Int(state.scale) }); err != nil {
			return err
		}
	}
	if state.transform != render.Normal || state.set&fieldTransform != 0 {
		if err := request(st, id, "set_buffer_transform", func(e *wire.Encoder) { e.Uint(uint32(state.transform)) }); err != nil {
			return err
		}
	}
	if !state.opaque.Empty() || state.set&fieldOpaque != 0 {
		if err := emitRegion(st, id, "set_opaque_region", state.opaque); err != nil {
			return err
		}
	}
	if v, ok := state.input.get(); ok {
		if err := emitRegion(st, id, "set_input_region", v); err != nil {
			return err
		}
	} else if state.set&fieldInput != 0 {
		if err := request(st, id, "set_input_region", func(e *wire.Encoder) { e.Object(0) }); err != nil {
			return err
		}
	}
	return nil
}

func emitRegion(st *server.Stream, surface wire.ObjectID, op string, r region.Region) error {
	compositor, err := st.Global(sw.WlCompositorInterface)
	if err != nil {
		return err
	}
	tmp := st.Temp()
	e, err := st.Request(compositor, sw.WlCompositorInterface, "create_region")
	if err != nil {
		return err
	}
	e.Object(tmp)
	if err := e.End(); err != nil {
		return err
	}
	for _, b := range r.Rects() {
		e, err := st.Request(tmp, sw.WlRegionInterface, "add")
		if err != nil {
			return err
		}
		e.Int(int32(b.Min.X)) //nolint:gosec // a region's rectangles came from int32 arguments
		e.Int(int32(b.Min.Y)) //nolint:gosec // a region's rectangles came from int32 arguments
		e.Int(int32(b.Dx()))  //nolint:gosec // a region's rectangles came from int32 arguments
		e.Int(int32(b.Dy()))  //nolint:gosec // a region's rectangles came from int32 arguments
		if err := e.End(); err != nil {
			return err
		}
	}
	if err := request(st, surface, op, func(e *wire.Encoder) { e.Object(tmp) }); err != nil {
		return err
	}
	e, err = st.Request(tmp, sw.WlRegionInterface, "destroy")
	if err != nil {
		return err
	}
	return e.End()
}

func aliveBuffer(b Buffer) (wire.ObjectID, error) {
	h, ok := b.(interface{ ResourceID() (wire.ObjectID, bool) })
	if !ok {
		return 0, fmt.Errorf("a %T buffer cannot be rebuilt", b)
	}
	id, alive := h.ResourceID()
	if !alive {
		return 0, errors.New("the client destroyed the buffer on screen, and only the renderer holds its pixels")
	}
	return id, nil
}

func (s *Surface) emitUncommitted(st *server.Stream) error {
	id := s.resource.Base().ID()
	if s.cache != nil {
		if err := s.emitPending(st, id, &s.cache.state); err != nil {
			return err
		}
		if err := request(st, id, "commit", nil); err != nil {
			return err
		}
	}
	return s.emitPending(st, id, &s.pending)
}

func (s *Surface) emitPending(st *server.Stream, id wire.ObjectID, state *state) error {
	if state.set&fieldBuffer != 0 {
		var buffer wire.ObjectID
		if state.buffer != nil {
			var err error
			if buffer, err = aliveBuffer(state.buffer); err != nil {
				return fmt.Errorf("wl_surface@%d: %w", id, err)
			}
		}
		if err := request(st, id, "attach", func(e *wire.Encoder) { e.Object(buffer); e.Int(0); e.Int(0) }); err != nil {
			return err
		}
	}
	if state.set&fieldOffset != 0 {
		if err := request(st, id, "offset", func(e *wire.Encoder) { e.Int(int32(state.offset.X)); e.Int(int32(state.offset.Y)) }); err != nil { //nolint:gosec // an offset came from int32 arguments
			return err
		}
	}
	for op, rects := range map[string]*region.Set{"damage": &state.damage, "damage_buffer": &state.bufferDamage} {
		for _, r := range rects.Rects() {
			if err := request(st, id, op, func(e *wire.Encoder) {
				e.Int(int32(r.Min.X)) //nolint:gosec // damage came from int32 arguments
				e.Int(int32(r.Min.Y)) //nolint:gosec // damage came from int32 arguments
				e.Int(int32(r.Dx()))  //nolint:gosec // damage came from int32 arguments
				e.Int(int32(r.Dy()))  //nolint:gosec // damage came from int32 arguments
			}); err != nil {
				return err
			}
		}
	}
	return s.emitState(st, state)
}

func (m *Manager) keepable(c *server.Client) string {
	surfaces, err := m.surfacesOf(c)
	if err != nil {
		return err.Error()
	}
	for _, s := range surfaces {
		if s.current.buffer == nil {
			continue
		}
		if _, err := aliveBuffer(s.current.buffer); err != nil {
			return fmt.Sprintf("wl_surface@%d: %v", s.resource.Base().ID(), err)
		}
	}
	return ""
}

func (m *Manager) Busy() string {
	for _, c := range m.display.Clients() {
		surfaces, err := m.surfacesOf(c)
		if err != nil {
			return err.Error()
		}
		for _, s := range surfaces {
			if len(s.queue) > 0 {
				return fmt.Sprintf("wl_surface@%d of pid %d has a commit waiting", s.resource.Base().ID(), c.Creds().Pid)
			}
		}
	}
	return ""
}

func (b *Backing) ResourceID() (wire.ObjectID, bool) {
	return b.resource.Base().ID(), !b.orphaned
}
