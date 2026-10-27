package xwayland

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/data"
	"github.com/stubbedev/lys-wm/internal/server"
)

const (
	incrChunk    = 64 << 10
	maxSelection = 64 << 20
	maxIncoming  = 32
	maxOutgoing  = 8
)

func mimeOf(atom string) (string, bool) {
	switch {
	case atom == "UTF8_STRING":
		return "text/plain;charset=utf-8", true
	case atom == "TEXT":
		return "text/plain", true
	case strings.Contains(atom, "/"):
		return atom, true
	}
	return "", false
}

func atomOf(mime string) (string, bool) {
	switch {
	case mime == "text/plain;charset=utf-8":
		return "UTF8_STRING", true
	case mime == "text/plain":
		return "TEXT", true
	case strings.Contains(mime, "/"):
		return mime, true
	}
	return "", false
}

type selection struct {
	kind    data.Kind
	atom    xproto.Atom
	x       *xSource
	wayland data.Source
	targets map[xproto.Atom]string
	owned   bool
}

type xSource struct {
	m     *wm
	sel   *selection
	mimes []string
	atoms map[string]xproto.Atom
}

func (s *xSource) Client() *server.Client {
	return s.m.client
}

func (s *xSource) Mimes() []string {
	return s.mimes
}

func (s *xSource) Send(mime string, fd int) error {
	return s.m.receive(s, mime, fd)
}

func (s *xSource) Cancel() error {
	if s.sel.x == s {
		s.sel.x = nil
	}
	return nil
}

type incoming struct {
	src  *xSource
	atom xproto.Atom
	out  *os.File
	incr bool
	busy bool
}

type outKey struct {
	requestor xproto.Window
	property  xproto.Atom
}

type outgoing struct {
	target  xproto.Atom
	src     *os.File
	pending []byte
	total   int
	reading bool
	waiting bool
	done    bool
	since   time.Time
}

func (o *outgoing) close() error {
	if o.src == nil {
		return nil
	}
	err := o.src.Close()
	o.src = nil
	return err
}

func (m *wm) outgoingBusy() bool {
	m.expireOutgoing()
	return m.reading+len(m.outgoing) >= maxOutgoing
}

func (m *wm) expireOutgoing() {
	m.dropOutgoing(func(k outKey, o *outgoing) bool {
		if time.Since(o.since) <= m.transfer {
			return false
		}
		m.report(fmt.Errorf("xwayland: the X window %d took nothing of a selection for %v; dropped the transfer", k.requestor, m.transfer))
		return true
	})
}

func (m *wm) reaped() error {
	m.expireOutgoing()
	m.armReap()
	return nil
}

func (m *wm) armReap() {
	if m.reap == nil {
		return
	}
	var err error
	if len(m.outgoing) == 0 {
		err = m.reap.Disarm()
	} else {
		oldest := slices.MinFunc(slices.Collect(maps.Values(m.outgoing)), func(a, b *outgoing) int { return a.since.Compare(b.since) })
		err = m.reap.Arm(max(time.Until(oldest.since.Add(m.transfer)), time.Millisecond))
	}
	if err != nil {
		m.report(fmt.Errorf("xwayland: watch the selection transfers to X: %w", err))
	}
}

func (m *wm) dropOutgoingTo(requestor xproto.Window) {
	m.dropOutgoing(func(k outKey, _ *outgoing) bool { return k.requestor == requestor })
}

func (m *wm) dropOutgoing(drop func(outKey, *outgoing) bool) {
	maps.DeleteFunc(m.outgoing, func(k outKey, o *outgoing) bool {
		if !drop(k, o) {
			return false
		}
		if err := o.close(); err != nil {
			m.report(fmt.Errorf("xwayland: close a dropped selection transfer to X: %w", err))
		}
		return true
	})
}

func (m *wm) selectionOf(atom xproto.Atom) *selection {
	for _, s := range m.selections {
		if s.atom == atom {
			return s
		}
	}
	return nil
}

func (m *wm) selectionOwned(atom xproto.Atom, owner xproto.Window, at xproto.Timestamp) error {
	sel := m.selectionOf(atom)
	if sel == nil || owner == m.sel {
		return nil
	}
	if owner == 0 {
		if sel.x == nil {
			return nil
		}
		sel.x = nil
		return m.listener.SetSelection(sel.kind, nil)
	}
	sel.owned = false
	if m.focused == 0 {
		return nil
	}
	xproto.ConvertSelection(m.x, m.sel, sel.atom, m.atoms[atomTargets], sel.atom, at)
	return nil
}

func (m *wm) targetsOffered(atom xproto.Atom, offered map[string]xproto.Atom) error {
	sel := m.selectionOf(atom)
	if sel == nil || len(offered) == 0 {
		return nil
	}
	src := &xSource{m: m, sel: sel, mimes: slices.Sorted(maps.Keys(offered)), atoms: offered}
	sel.x = src
	return m.listener.SetSelection(sel.kind, src)
}

func pollable(fd int, name string) (*os.File, error) {
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, errors.Join(fmt.Errorf("make the %s fd non-blocking: %w", name, err), unix.Close(fd))
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (m *wm) receive(src *xSource, mime string, fd int) error {
	own, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("xwayland: keep the paste fd: %w", err)
	}
	out, err := pollable(own, "selection")
	if err != nil {
		return fmt.Errorf("xwayland: paste from the X selection: %w", err)
	}
	atom, ok := src.atoms[mime]
	if !ok || src.sel.x != src {
		m.report(errors.Join(fmt.Errorf("xwayland: the X selection no longer offers %s", mime), out.Close()))
		return nil
	}
	if len(m.incoming) >= maxIncoming {
		m.report(errors.Join(fmt.Errorf("xwayland: %d X selection transfers are already waiting; dropped %s", maxIncoming, mime), out.Close()))
		return nil
	}
	m.incoming = append(m.incoming, &incoming{src: src, atom: atom, out: out})
	if len(m.incoming) == 1 {
		m.convert()
	}
	return nil
}

func (m *wm) convert() {
	if len(m.incoming) == 0 {
		m.watchTransfer(false)
		return
	}
	in := m.incoming[0]
	xproto.ConvertSelection(m.x, m.sel, in.src.sel.atom, in.atom, m.atoms[atomWLSelection], xproto.TimeCurrentTime)
	m.watchTransfer(true)
}

func (m *wm) watchTransfer(on bool) {
	if m.stall == nil {
		return
	}
	var err error
	if on {
		err = m.stall.Arm(m.transfer)
	} else {
		err = m.stall.Disarm()
	}
	if err != nil {
		m.report(fmt.Errorf("xwayland: watch an X selection transfer: %w", err))
	}
}

func (m *wm) stalled() error {
	if len(m.incoming) > 0 {
		m.finishIncoming(fmt.Errorf("the X owner sent nothing for %v", m.transfer))
	}
	return nil
}

func (m *wm) finishIncoming(err error) {
	in := m.incoming[0]
	m.incoming = m.incoming[1:]
	xproto.DeleteProperty(m.x, m.sel, m.atoms[atomWLSelection])
	if err = errors.Join(err, in.out.Close()); err != nil {
		m.report(fmt.Errorf("xwayland: paste from the X selection: %w", err))
	}
	m.convert()
}

func (m *wm) converted(target xproto.Atom, typ xproto.Atom, value []byte, ok bool) error {
	if len(m.incoming) == 0 || m.incoming[0].atom != target || m.incoming[0].incr {
		return nil
	}
	in := m.incoming[0]
	switch {
	case !ok:
		m.finishIncoming(errors.New("the X owner refused the conversion"))
		return nil
	case typ == m.atoms[atomIncr]:
		in.incr = true
		xproto.DeleteProperty(m.x, m.sel, m.atoms[atomWLSelection])
		return nil
	}
	return m.write(in, value, true)
}

func (m *wm) chunk(typ xproto.Atom, value []byte) error {
	if len(m.incoming) == 0 || !m.incoming[0].incr || typ == m.atoms[atomIncr] || m.incoming[0].busy {
		return nil
	}
	in := m.incoming[0]
	if len(value) == 0 {
		m.finishIncoming(nil)
		return nil
	}
	return m.write(in, value, false)
}

func (m *wm) write(in *incoming, value []byte, last bool) error {
	in.busy = true
	out := in.out
	go func() {
		err := out.SetWriteDeadline(time.Now().Add(m.transfer))
		if err == nil {
			_, err = out.Write(value)
		}
		m.post(func() error {
			if len(m.incoming) == 0 || m.incoming[0] != in {
				return nil
			}
			in.busy = false
			m.watchTransfer(true)
			if err != nil || last {
				m.finishIncoming(err)
				return nil
			}
			xproto.DeleteProperty(m.x, m.sel, m.atoms[atomWLSelection])
			return nil
		})
	}()
	return nil
}

func (m *wm) selectionChanged(k data.Kind, src data.Source) error {
	sel := m.selections[k]
	if _, ours := src.(*xSource); ours {
		return nil
	}
	sel.wayland, sel.targets = src, nil
	if sel.owned {
		sel.owned = false
		xproto.SetSelectionOwner(m.x, 0, sel.atom, xproto.TimeCurrentTime)
	}
	if src == nil {
		return nil
	}
	names := map[string]string{}
	for _, mime := range src.Mimes() {
		if name, ok := atomOf(mime); ok {
			names[name] = mime
		}
	}
	go func() {
		sorted := slices.Sorted(maps.Keys(names))
		atoms, err := intern(m.x, sorted)
		m.post(func() error {
			if err != nil {
				m.report(fmt.Errorf("xwayland: offer the Wayland selection to X: %w", err))
				return nil
			}
			if sel.wayland != src {
				return nil
			}
			sel.targets = make(map[xproto.Atom]string, len(atoms))
			for i, a := range atoms {
				sel.targets[a] = names[sorted[i]]
			}
			sel.owned = true
			xproto.SetSelectionOwner(m.x, m.sel, sel.atom, xproto.TimeCurrentTime)
			return nil
		})
	}()
	return nil
}

func (m *wm) notifySelection(e xproto.SelectionRequestEvent, property xproto.Atom) {
	ev := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: property}
	xproto.SendEvent(m.x, false, e.Requestor, xproto.EventMaskNoEvent, string(ev.Bytes()))
}

func (m *wm) requested(e xproto.SelectionRequestEvent) error {
	sel := m.selectionOf(e.Selection)
	property := cmp.Or(e.Property, e.Target)
	if sel == nil || sel.wayland == nil || !sel.owned {
		m.notifySelection(e, 0)
		return nil
	}
	if e.Target == m.atoms[atomTargets] {
		list := append([]xproto.Atom{m.atoms[atomTargets]}, slices.Sorted(maps.Keys(sel.targets))...)
		m.changeAtoms(e.Requestor, property, xproto.AtomAtom, list)
		m.notifySelection(e, property)
		return nil
	}
	mime, ok := sel.targets[e.Target]
	if !ok {
		m.notifySelection(e, 0)
		return nil
	}
	if m.outgoingBusy() {
		m.notifySelection(e, 0)
		m.report(fmt.Errorf("xwayland: %d transfers to X are already in flight; refused one for %s", maxOutgoing, mime))
		return nil
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		m.notifySelection(e, 0)
		m.report(fmt.Errorf("xwayland: pipe for a selection request: %w", err))
		return nil
	}
	in, err := pollable(p[0], "selection")
	if err != nil {
		m.notifySelection(e, 0)
		m.report(errors.Join(fmt.Errorf("xwayland: pipe for a selection request: %w", err), unix.Close(p[1])))
		return nil
	}
	err = sel.wayland.Send(mime, p[1])
	if err = errors.Join(err, unix.Close(p[1])); err != nil {
		m.notifySelection(e, 0)
		m.report(errors.Join(fmt.Errorf("xwayland: ask the Wayland selection for %s: %w", mime, err), in.Close()))
		return nil
	}
	m.reading++
	go func() {
		chunk, ended, err := readChunk(in, m.transfer)
		m.post(func() error {
			m.reading--
			return m.firstChunk(e, property, in, chunk, ended, err)
		})
	}()
	return nil
}

func readChunk(in *os.File, timeout time.Duration) ([]byte, bool, error) {
	if err := in.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, false, err
	}
	chunk := make([]byte, incrChunk)
	n, err := io.ReadFull(in, chunk)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return chunk[:n], true, nil
	}
	return chunk, false, err
}

func (m *wm) firstChunk(e xproto.SelectionRequestEvent, property xproto.Atom, in *os.File, chunk []byte, ended bool, err error) error {
	if err != nil {
		m.notifySelection(e, 0)
		m.report(errors.Join(fmt.Errorf("xwayland: copy the Wayland selection to X: %w", err), in.Close()))
		return nil
	}
	if ended {
		xproto.ChangeProperty(m.x, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(chunk)), chunk) //nolint:gosec // at most one chunk
		m.notifySelection(e, property)
		if err := in.Close(); err != nil {
			m.report(fmt.Errorf("xwayland: close a selection transfer to X: %w", err))
		}
		return nil
	}
	xproto.ChangeWindowAttributes(m.x, e.Requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
	lowerBound := binary.LittleEndian.AppendUint32(nil, incrChunk)
	xproto.ChangeProperty(m.x, xproto.PropModeReplace, e.Requestor, property, m.atoms[atomIncr], 32, 1, lowerBound)
	m.outgoing[outKey{e.Requestor, property}] = &outgoing{target: e.Target, src: in, pending: chunk, total: len(chunk), since: time.Now()}
	m.armReap()
	m.notifySelection(e, property)
	return nil
}

func (m *wm) propertyDeleted(win xproto.Window, property xproto.Atom) error {
	key := outKey{win, property}
	o, ok := m.outgoing[key]
	if !ok {
		return nil
	}
	if o.done {
		m.dropOutgoing(func(k outKey, _ *outgoing) bool { return k == key })
		return nil
	}
	o.waiting = true
	m.sendChunk(key, o)
	return nil
}

func (m *wm) sendChunk(key outKey, o *outgoing) {
	if !o.waiting || o.reading {
		return
	}
	if len(o.pending) == 0 && o.src != nil {
		m.readMore(key, o)
		return
	}
	n := len(o.pending)
	xproto.ChangeProperty(m.x, xproto.PropModeReplace, key.requestor, key.property, o.target, 8, uint32(n), o.pending) //nolint:gosec // at most one chunk
	o.pending, o.waiting, o.done, o.since = nil, false, n == 0, time.Now()
	m.armReap()
}

func (m *wm) readMore(key outKey, o *outgoing) {
	o.reading = true
	src := o.src
	go func() {
		chunk, ended, err := readChunk(src, m.transfer)
		m.post(func() error {
			o.reading = false
			if m.outgoing[key] != o {
				return nil
			}
			o.total += len(chunk)
			if err == nil && o.total > maxSelection {
				err = fmt.Errorf("the selection is larger than %d bytes", maxSelection)
			}
			if err != nil {
				m.dropOutgoing(func(k outKey, _ *outgoing) bool { return k == key })
				m.report(fmt.Errorf("xwayland: copy the Wayland selection to X: %w", err))
				return nil
			}
			o.pending = chunk
			if ended {
				if err := o.close(); err != nil {
					m.report(fmt.Errorf("xwayland: close a selection transfer to X: %w", err))
				}
			}
			m.sendChunk(key, o)
			return nil
		})
	}()
}
