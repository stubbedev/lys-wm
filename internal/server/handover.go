package server

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/loop"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/wire"
)

const deleteIDOpcode wire.Opcode = 1

type InertObject struct {
	ID      uint32
	Iface   string
	Version uint32
}

type GlobalState struct {
	Name    uint32
	Iface   string
	Key     string
	Version uint32
}

type ClientState struct {
	FD        int
	PID       int32
	LastID    uint32
	TempFirst uint32
	Inert     []InertObject
	Stream    []byte
	StreamFDs []int
	In        []byte
	InFDs     []int
	Out       []byte
	OutFDs    []int
}

type State struct {
	Name     string
	Path     string
	ListenFD int
	LockFD   int
	Serial   uint32
	Globals  []GlobalState
	Clients  []ClientState
}

type Unkeepable struct {
	PID    int32
	Reason string
}

type Stream struct {
	c        *Client
	conn     *wire.Conn
	next     wire.ObjectID
	registry wire.ObjectID
	bound    map[*wire.Interface]wire.ObjectID
	inert    map[wire.ObjectID]struct{}
	renames  map[*record]wire.ObjectID
	emitted  map[wire.ObjectID]bool
}

func (s *Stream) Emitted(r wire.Resource) {
	s.emitted[r.Base().ID()] = true
}

func (d *Display) OnHandover(f func(c *Client, s *Stream) error) {
	d.handover = append(d.handover, f)
}

func (d *Display) OnPrepareHandover(f func(c *Client, s *Stream) error) {
	d.prepare = append(d.prepare, f)
}

func (d *Display) OnRetire(f func(c *Client, out *wire.Conn) error) {
	d.retire = append(d.retire, f)
}

func (d *Display) CheckKeepable(f func(c *Client) string) {
	d.checks = append(d.checks, f)
}

func (g *Global) SetKey(key string) {
	g.key = key
}

func (c *Client) Objects() []wire.Resource {
	ids := slices.Sorted(maps.Keys(c.objects))
	out := make([]wire.Resource, 0, len(ids))
	for _, id := range ids {
		if r := c.objects[id]; r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (c *Client) Index() (int, error) {
	i := slices.Index(c.display.clients, c)
	if i < 0 {
		return 0, fmt.Errorf("server: the client of pid %d has left", c.creds.Pid)
	}
	return i, nil
}

func (d *Display) Unkeepable(skip func(*Client) bool) []Unkeepable {
	var out []Unkeepable
	for _, c := range d.clients {
		if skip(c) {
			continue
		}
		reasons := map[string]bool{}
		if c.context != nil {
			reasons["it runs in the sandbox of "+c.context.AppID] = true
		}
		for _, r := range c.Objects() {
			if why, ok := refused[r.Base().Interface().Name]; ok {
				reasons[why+" ("+r.Base().Interface().Name+")"] = true
			}
		}
		for _, check := range d.checks {
			if why := check(c); why != "" {
				reasons[why] = true
			}
		}
		for _, why := range slices.Sorted(maps.Keys(reasons)) {
			out = append(out, Unkeepable{PID: c.creds.Pid, Reason: why})
		}
	}
	for _, sb := range d.sandboxes {
		out = append(out, Unkeepable{Reason: "a sandbox listener of " + sb.ctx.AppID + " is open"})
	}
	return out
}

func (d *Display) Park() error {
	d.parked = true
	errs := []error{d.listener.Modify(0)}
	for _, c := range d.clients {
		if err := c.flush(); err != nil {
			d.dead = append(d.dead, c)
		}
		errs = append(errs, c.source.Modify(0))
	}
	return errors.Join(errs...)
}

func (d *Display) Resume() error {
	d.parked = false
	errs := []error{d.listener.Modify(loop.Readable)}
	for _, c := range slices.Clone(d.clients) {
		errs = append(errs, c.source.Modify(loop.Readable))
	}
	return errors.Join(errs...)
}

func (s *Stream) Temp() wire.ObjectID {
	id := s.next
	s.next++
	return id
}

func (s *Stream) Request(id wire.ObjectID, iface *wire.Interface, name string) (*wire.Encoder, error) {
	i := slices.IndexFunc(iface.Requests, func(spec wire.MessageSpec) bool { return spec.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("server: %s has no request %s", iface.Name, name)
	}
	return s.conn.Begin(id, wire.Opcode(i)), nil //nolint:gosec // an opcode is an index into a generated table
}

func (s *Stream) Global(iface *wire.Interface) (wire.ObjectID, error) {
	if id, ok := s.bound[iface]; ok {
		return id, nil
	}
	if s.registry == 0 {
		s.registry = s.Temp()
		e, err := s.Request(1, sw.WlDisplayInterface, "get_registry")
		if err != nil {
			return 0, err
		}
		e.Object(s.registry)
		if err := e.End(); err != nil {
			return 0, err
		}
	}
	i := slices.IndexFunc(s.c.display.globals, func(g *Global) bool { return g.iface == iface && !g.removed })
	if i < 0 {
		return 0, fmt.Errorf("server: no global %s to rebuild a client's objects with", iface.Name)
	}
	g := s.c.display.globals[i]
	id := s.Temp()
	e, err := s.Request(s.registry, sw.WlRegistryInterface, "bind")
	if err != nil {
		return 0, err
	}
	e.Uint(g.name)
	e.Str(iface.Name)
	e.Uint(g.version)
	e.Object(id)
	if err := e.End(); err != nil {
		return 0, err
	}
	s.bound[iface] = id
	return id, nil
}

func (s *Stream) MarkInert(r wire.Resource) {
	s.inert[r.Base().ID()] = struct{}{}
}

func (j *journal) reachable() []*record {
	seen := map[*record]bool{}
	var visit func(*record)
	visitMsg := func(m *retained) {
		if m == nil {
			return
		}
		visit(m.target)
		for _, a := range m.refs {
			if a.rec != nil {
				visit(a.rec)
			}
		}
	}
	visit = func(r *record) {
		if r == nil || seen[r] {
			return
		}
		seen[r] = true
		visitMsg(r.creator)
		for _, m := range r.msgs {
			visitMsg(m)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(j.records)) {
		visit(j.records[id])
	}
	return slices.Collect(maps.Keys(seen))
}

func (s *Stream) journal() error {
	records := s.c.journal.reachable()
	msgs := make([]*retained, 0, len(records))
	for _, r := range records {
		if r.dead && r.id != 1 {
			s.renames[r] = s.Temp()
		}
		if r.creator == nil && r.id != 1 {
			continue
		}
		if _, gone := s.inert[r.id]; gone && !r.dead {
			continue
		}
		if r.creator != nil {
			msgs = append(msgs, r.creator)
		}
		msgs = append(msgs, r.msgs...)
		if r.dead && r.end != nil {
			msgs = append(msgs, r.end)
		}
	}
	slices.SortFunc(msgs, func(a, b *retained) int { return int(a.seq) - int(b.seq) }) //nolint:gosec // a client cannot reach 2^63 requests
	for _, m := range msgs {
		for range max(m.count, 1) {
			if err := s.message(m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Stream) message(m *retained) error {
	for _, b := range m.bare {
		r, live := s.c.objects[b.id]
		if !live || b.iface != "" && r.Base().Interface().Name != b.iface {
			return nil
		}
	}
	data := slices.Clone(m.data)
	if id, ok := s.renames[m.target]; ok {
		wire.SetObjectAt(data, 0, id)
	}
	for _, a := range m.refs {
		if a.rec == nil {
			continue
		}
		if id, ok := s.renames[a.rec]; ok {
			wire.SetObjectAt(data, a.off, id)
		}
	}
	return s.conn.Raw(data, m.fds)
}

func (s *Stream) markInert() {
	for _, r := range s.c.Objects() {
		if s.c.journal.policy(r.Base().Interface()).class == classInert {
			s.inert[r.Base().ID()] = struct{}{}
		}
	}
}

func (c *Client) retirement(out *wire.Conn) error {
	for _, r := range c.Objects() {
		id, iface, version := r.Base().ID(), r.Base().Interface(), r.Base().Version()
		p := c.journal.policy(iface)
		for _, name := range p.retire {
			i := slices.IndexFunc(iface.Events, func(spec wire.MessageSpec) bool { return spec.Name == name })
			if i < 0 {
				return fmt.Errorf("server: %s has no event %s to retire it with", iface.Name, name)
			}
			if iface.Events[i].Since > version {
				continue
			}
			e := out.Begin(id, wire.Opcode(i)) //nolint:gosec // an opcode is an index into a generated table
			for _, a := range iface.Events[i].Args {
				switch a.Type {
				case wire.ArgString:
					e.Str("")
				case wire.ArgArray:
					e.Array(nil)
				default:
					e.Uint(0)
				}
			}
			if err := e.End(); err != nil {
				return err
			}
		}
		if p.class == classGone && !id.ServerSide() {
			e := out.Begin(1, deleteIDOpcode)
			e.Uint(uint32(id))
			if err := e.End(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Display) Snapshot(t *handover.FDs, skip func(*Client) bool) (*State, map[*Client]int, error) {
	kept := map[*Client]int{}
	st := &State{Name: d.name, Path: d.socketPath, ListenFD: t.Add(d.listenFD), LockFD: t.Add(d.lockFD), Serial: d.serial}
	for _, g := range d.globals {
		if !g.removed {
			st.Globals = append(st.Globals, GlobalState{Name: g.name, Iface: g.iface.Name, Key: g.key, Version: g.version})
		}
	}
	for _, c := range d.clients {
		if skip(c) {
			continue
		}
		cs, err := c.snapshot(t)
		if err != nil {
			return nil, nil, fmt.Errorf("server: the client of pid %d: %w", c.creds.Pid, err)
		}
		kept[c] = len(st.Clients)
		st.Clients = append(st.Clients, cs)
	}
	return st, kept, nil
}

func (c *Client) snapshot(t *handover.FDs) (ClientState, error) {
	s := &Stream{c: c, conn: wire.NewRecorder(), next: c.lastID + 1, bound: map[*wire.Interface]wire.ObjectID{}, inert: map[wire.ObjectID]struct{}{}, renames: map[*record]wire.ObjectID{}, emitted: map[wire.ObjectID]bool{}}
	first := s.next
	s.markInert()
	for _, f := range c.display.prepare {
		if err := f(c, s); err != nil {
			return ClientState{}, err
		}
	}
	for _, r := range c.Objects() {
		if _, ok := r.(*sw.WlBuffer); ok && !s.emitted[r.Base().ID()] {
			return ClientState{}, fmt.Errorf("wl_buffer@%d cannot be rebuilt", r.Base().ID())
		}
	}
	if err := s.journal(); err != nil {
		return ClientState{}, err
	}
	for _, f := range c.display.handover {
		if err := f(c, s); err != nil {
			return ClientState{}, err
		}
	}
	stream, streamFDs := s.conn.Recorded()
	in, inFDs, out, outFDs := c.conn.Pending()
	retire := wire.NewRecorder()
	if err := c.retirement(retire); err != nil {
		return ClientState{}, err
	}
	for _, f := range c.display.retire {
		if err := f(c, retire); err != nil {
			return ClientState{}, err
		}
	}
	extra, extraFDs := retire.Recorded()
	cs := ClientState{
		FD: t.Add(c.conn.FD()), PID: c.creds.Pid, LastID: uint32(s.next - 1), TempFirst: uint32(first),
		Stream: stream, StreamFDs: t.AddOwned(streamFDs), In: in, InFDs: t.AddAll(inFDs),
		Out: append(out, extra...), OutFDs: t.AddAll(append(outFDs, extraFDs...)),
	}
	for _, id := range slices.Sorted(maps.Keys(s.inert)) {
		r := c.objects[id]
		cs.Inert = append(cs.Inert, InertObject{ID: uint32(id), Iface: r.Base().Interface().Name, Version: r.Base().Version()})
	}
	return cs, nil
}

type restoring struct {
	failed error
}

func (d *Display) restoreFailed(c *Client, err error) {
	if d.restored != nil && d.restored.failed == nil {
		d.restored.failed = fmt.Errorf("server: rebuild the client of pid %d: %w", c.creds.Pid, err)
	}
}

func Adopt(l *loop.Loop, opts Options, st *State, t *handover.FDs) (*Display, error) {
	if opts.Report == nil {
		return nil, errors.New("server: Options.Report is required")
	}
	listenFD, err := t.Get(st.ListenFD)
	if err != nil {
		return nil, err
	}
	lockFD, err := t.Get(st.LockFD)
	if err != nil {
		return nil, err
	}
	d := &Display{
		loop: l, trace: opts.Trace, report: opts.Report, start: time.Now(), listenFD: listenFD, lockFD: lockFD, spare: -1,
		policies: map[*wire.Interface]*objectPolicy{}, name: st.Name, socketPath: st.Path, lockPath: st.Path + ".lock", bound: true, serial: st.Serial, journaling: true,
	}
	if d.spare, err = openSpare(); err != nil {
		return nil, err
	}
	if d.listener, err = l.AddFD(listenFD, 0, d.accept); err != nil {
		return nil, errors.Join(err, unix.Close(d.spare))
	}
	l.BeforeWait(d.flush)
	d.parked = true
	return d, nil
}

func (d *Display) renameGlobals(old []GlobalState) ([]*Global, []uint32) {
	taken := map[uint32]bool{}
	var extra []*Global
	seen := map[*Global]bool{}
	var removed []uint32
	maximum := uint32(0)
	for _, g := range old {
		maximum = max(maximum, g.Name)
	}
	for _, o := range old {
		i := slices.IndexFunc(d.globals, func(g *Global) bool { return g.iface.Name == o.Iface && g.key == o.Key && !seen[g] })
		if i < 0 {
			removed = append(removed, o.Name)
			continue
		}
		seen[d.globals[i]] = true
		d.globals[i].name = o.Name
		taken[o.Name] = true
	}
	next := maximum
	for _, g := range d.globals {
		if !seen[g] {
			next++
			g.name = next
			extra = append(extra, g)
		}
	}
	d.nextGlobal = next
	return extra, removed
}

func (d *Display) Restore(st *State, t *handover.FDs) (func() error, error) {
	extra, removed := d.renameGlobals(st.Globals)
	d.restored = &restoring{}
	clients := make([]*Client, 0, len(st.Clients))
	feeds := make([]func() error, 0, len(st.Clients))
	for i := range st.Clients {
		cs := &st.Clients[i]
		fd, err := t.Get(cs.FD)
		if err != nil {
			return nil, err
		}
		c, err := d.addClient(fd, nil)
		if err != nil {
			return nil, err
		}
		c.conn.Mute()
		c.restoring = true
		c.lastID = wire.ObjectID(cs.LastID)
		if err := c.source.Modify(0); err != nil {
			return nil, err
		}
		for _, o := range cs.Inert {
			iface, ok := inertInterfaces[o.Iface]
			if !ok {
				return nil, fmt.Errorf("server: the client of pid %d holds %s, which cannot be retired here", cs.PID, o.Iface)
			}
			if err := c.addInert(wire.ObjectID(o.ID), iface, o.Version); err != nil {
				return nil, err
			}
		}
		streamFDs, err := t.GetAll(cs.StreamFDs)
		if err != nil {
			return nil, err
		}
		dup := make([]int, len(streamFDs))
		for j, sfd := range streamFDs {
			if dup[j], err = unix.FcntlInt(uintptr(sfd), unix.F_DUPFD_CLOEXEC, 0); err != nil {
				return nil, fmt.Errorf("server: duplicate a fd of the client of pid %d: %w", cs.PID, err)
			}
		}
		if err := c.conn.Feed(cs.Stream, dup); err != nil {
			return nil, err
		}
		if err := c.dispatch(); err != nil {
			return nil, err
		}
		if d.restored.failed != nil {
			return nil, d.restored.failed
		}
		c.dropTemps(wire.ObjectID(cs.TempFirst), wire.ObjectID(cs.LastID))
		c.restoring = false
		clients = append(clients, c)
		feeds = append(feeds, c.releaser(cs, t))
	}
	d.ackFloor = d.serial
	return func() error {
		var errs []error
		for _, f := range feeds {
			errs = append(errs, f())
		}
		d.parked = false
		for _, c := range clients {
			for _, name := range removed {
				for _, r := range c.registries {
					errs = append(errs, r.SendGlobalRemove(name))
				}
			}
			for _, g := range extra {
				if g.visible(c) {
					for _, r := range c.registries {
						errs = append(errs, r.SendGlobal(g.name, g.iface.Name, g.version))
					}
				}
			}
		}
		for _, c := range clients {
			errs = append(errs, c.dispatch())
		}
		errs = append(errs, d.listener.Modify(loop.Readable), d.flush())
		return errors.Join(errs...)
	}, nil
}

func (c *Client) dropTemps(first, last wire.ObjectID) {
	c.registries = slices.DeleteFunc(c.registries, func(r *sw.WlRegistry) bool {
		return r.Base().ID() >= first && r.Base().ID() <= last
	})
	for _, id := range slices.Sorted(maps.Keys(c.objects)) {
		if id >= first && id <= last {
			r := c.objects[id]
			r.Base().MarkRemoved()
			_ = c.runHooks(id)
			delete(c.objects, id)
			c.journal.removed(id)
		}
	}
}

func (c *Client) releaser(cs *ClientState, t *handover.FDs) func() error {
	return func() error {
		outFDs, err := t.GetAll(cs.OutFDs)
		if err != nil {
			return err
		}
		inFDs, err := t.GetAll(cs.InFDs)
		if err != nil {
			return err
		}
		err = c.conn.Unmute(func(id wire.ObjectID, opcode wire.Opcode) (bool, int) { return c.keepHeld(id, opcode) }, cs.Out, outFDs)
		if err != nil {
			return err
		}
		return errors.Join(c.conn.Feed(cs.In, inFDs), c.source.Modify(loop.Readable))
	}
}

func (c *Client) keepHeld(id wire.ObjectID, opcode wire.Opcode) (bool, int) {
	r, ok := c.objects[id]
	if !ok {
		return false, 0
	}
	iface := r.Base().Interface()
	if int(opcode) >= len(iface.Events) {
		return false, 0
	}
	spec := iface.Events[opcode]
	n := 0
	for _, a := range spec.Args {
		if a.Type == wire.ArgFD {
			n++
		}
	}
	return !slices.Contains(dropEvents[iface.Name], spec.Name), n
}

func (d *Display) StaleAck(serial uint32) bool {
	return d.ackFloor != 0 && serial <= d.ackFloor
}

func Event(out *wire.Conn, r wire.Resource, name string, write func(*wire.Encoder)) error {
	iface := r.Base().Interface()
	i := slices.IndexFunc(iface.Events, func(spec wire.MessageSpec) bool { return spec.Name == name })
	if i < 0 {
		return fmt.Errorf("server: %s has no event %s", iface.Name, name)
	}
	e := out.Begin(r.Base().ID(), wire.Opcode(i)) //nolint:gosec // an opcode is an index into a generated table
	if write != nil {
		write(e)
	}
	return e.End()
}

func (d *Display) KeepHandoverJournal() {
	d.journaling = true
}

func (d *Display) Serial() uint32 {
	return d.serial
}

func (d *Display) Clients() []*Client {
	return d.clients
}

func (c *Client) Restoring() bool {
	return c.restoring
}
