package compositor

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/syncobj"
)

const (
	waitSweep   = time.Second / 4
	waitStalled = 2
	stallDumps  = 8
)

type waitKind uint8

const (
	waitAcquire waitKind = iota + 1
	waitWriter
	waitRelease
	waitReleasePoint
)

var waitNames = [...]string{
	waitAcquire:      "its syncobj acquire point",
	waitWriter:       "its writer's implicit fence",
	waitRelease:      "a display plane to retire it",
	waitReleasePoint: "the kernel to reach it",
}

func (k waitKind) String() string {
	return waitNames[k]
}

type waitKey struct {
	kind    waitKind
	surface *surface.Surface
	buffer  *dmabuf.Buffer
	release syncobj.Release
}

type waitEpisode struct {
	subject string
	born    uint64
	seen    uint64
}

type stallDump struct {
	sweep uint64
	count uint64
	path  string
}

type waitWatch struct {
	timer   *loop.Timer
	armed   bool
	sweeps  uint64
	stalled map[waitKey]waitEpisode
	dump    stallDump
}

func waitedOver(sweeps uint64) time.Duration {
	return time.Duration(sweeps-1) * waitSweep //nolint:gosec // a quarter-second count reaches int64 only after billions of years
}

func (c *Compositor) Awaiting() error {
	return c.armWaits()
}

func (c *Compositor) armWaits() error {
	if c.waits.armed || c.waits.timer == nil {
		return nil
	}
	c.waits.armed = true
	return c.waits.timer.Arm(waitSweep)
}

func (c *Compositor) sweepWaits() error {
	w := &c.waits
	w.armed = false
	w.sweeps++
	busy := c.sweepWithheld()
	if c.syncobj != nil {
		owing, err := c.syncobj.Sweep(c.acquireWaiting, c.releaseOwed)
		if err != nil {
			c.report(err)
		}
		busy = busy || owing
	}
	if c.dmabuf != nil && c.dmabuf.Sweep(c.writerWaiting) {
		busy = true
	}
	for k, e := range w.stalled {
		if e.seen == w.sweeps {
			continue
		}
		delete(w.stalled, k)
		c.report(fmt.Errorf("compositor: %s no longer waits for %s, after over %v", e.subject, k.kind, waitedOver(e.seen-e.born)))
	}
	if !busy {
		return nil
	}
	return c.armWaits()
}

func (c *Compositor) stillStalled(k waitKey) bool {
	e, ok := c.waits.stalled[k]
	if !ok {
		return false
	}
	e.seen = c.waits.sweeps
	c.waits.stalled[k] = e
	return true
}

func (c *Compositor) stall(k waitKey, subject string, sweeps uint64) {
	if c.waits.stalled == nil {
		c.waits.stalled = map[waitKey]waitEpisode{}
	}
	c.waits.stalled[k] = waitEpisode{subject: subject, born: c.waits.sweeps - sweeps, seen: c.waits.sweeps}
}

func (c *Compositor) protocolAtStall() string {
	if !c.display.RecordingProtocol() {
		return ""
	}
	w := &c.waits
	if w.dump.sweep != w.sweeps {
		path, err := c.stallDumpPath(w.dump.count % stallDumps)
		if err == nil {
			err = c.dumpProtocol(path)
		}
		if err != nil {
			c.report(fmt.Errorf("compositor: dump the protocol of a stall: %w", err))
			return ""
		}
		w.dump = stallDump{sweep: w.sweeps, count: w.dump.count + 1, path: path}
	}
	return "; the protocol recorded up to this moment is in " + w.dump.path
}

func (c *Compositor) stallDumpPath(n uint64) (string, error) {
	socket, err := ipc.SocketPath(filepath.Dir(c.display.Path()), c.display.Name())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s.stall-%d.log", strings.TrimSuffix(socket, filepath.Ext(socket)), n), nil
}

func (c *Compositor) acquireWaiting(s *surface.Surface, acquire, sweeps uint64) {
	k := waitKey{kind: waitAcquire, surface: s}
	if sweeps < waitStalled || c.stillStalled(k) {
		return
	}
	subject := c.describeSurface(s)
	c.stall(k, subject, sweeps)
	c.report(fmt.Errorf("compositor: %s has waited over %v for acquire point %d of its syncobj timeline; none of its commits since apply until the client's GPU work signals it%s", subject, waitedOver(sweeps), acquire, c.protocolAtStall()))
}

func (c *Compositor) writerWaiting(b *dmabuf.Buffer, s *surface.Surface, sweeps uint64) {
	k := waitKey{kind: waitWriter, surface: s, buffer: b}
	if sweeps < waitStalled || c.stillStalled(k) {
		return
	}
	subject := fmt.Sprintf("%s on %s", b, c.describeSurface(s))
	c.stall(k, subject, sweeps)
	c.report(fmt.Errorf("compositor: a commit of %s has waited over %v for its writer's implicit fence; it and every later commit of its surface apply only once the fence signals%s", subject, waitedOver(sweeps), c.protocolAtStall()))
}

func (c *Compositor) releaseOwed(r syncobj.Release, sweeps uint64) {
	k := waitKey{kind: waitReleasePoint, release: r}
	if sweeps < waitStalled || c.stillStalled(k) {
		return
	}
	subject := fmt.Sprintf("%s of %s on %s", r, r.Buffer(), c.describeSurface(r.Surface()))
	c.stall(k, subject, sweeps)
	c.report(fmt.Errorf("compositor: %s has not signalled over %v after lys-wm let the buffer go; it waits for %s, and until it signals the client cannot reuse the buffer%s", subject, waitedOver(sweeps), r.Awaits(), c.protocolAtStall()))
}

func (c *Compositor) sweepWithheld() bool {
	busy := false
	for b, held := range c.scannedOut {
		if !held.owed() {
			continue
		}
		busy = true
		if sweeps := c.waits.sweeps - held.owedSince; sweeps >= waitStalled {
			c.withheld(b, held, sweeps)
		}
	}
	return busy
}

func (c *Compositor) withheld(b *dmabuf.Buffer, held *scannedOut, sweeps uint64) {
	k := waitKey{kind: waitRelease, buffer: b}
	if c.stillStalled(k) {
		return
	}
	subject := fmt.Sprintf("%s, last shown on %s", b, c.describeSurface(held.surface))
	c.stall(k, subject, sweeps)
	c.report(fmt.Errorf("compositor: %s, has left its surface over %v ago but not the screen: its %s waits for a display plane to retire it%s", subject, waitedOver(sweeps), held.owes(), c.protocolAtStall()))
}

func (c *Compositor) describeSurface(s *surface.Surface) string {
	d := fmt.Sprintf("wl_surface@%d of pid %d", s.Resource().Base().ID(), s.Client().Creds().PID)
	if w := c.windowWithSurface(s.Root()); w != nil {
		return fmt.Sprintf("%s (app_id %q)", d, w.top.AppID())
	}
	return d
}

func (c *Compositor) owe(held *scannedOut) error {
	if held.owed() {
		return nil
	}
	held.owedSince = c.waits.sweeps
	return c.armWaits()
}

func (h *scannedOut) owed() bool {
	return h.released != nil || len(h.then) > 0
}

func (h *scannedOut) owes() string {
	switch {
	case h.released != nil && len(h.then) > 0:
		return "wl_buffer.release and syncobj release point"
	case h.released != nil:
		return "wl_buffer.release"
	}
	return "syncobj release point"
}
