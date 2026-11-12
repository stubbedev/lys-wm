// Package session gives lys-wm its seat: devices through logind, VT
// switching, and device discovery.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/loop"
)

const defaultSystem = "unix:path=/run/dbus/system_bus_socket"

type Device = logind.Device

func DeviceOf(path string) (Device, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return Device{}, fmt.Errorf("session: stat %s: %w", path, err)
	}
	return deviceOf(&st, path)
}

func deviceOf(st *unix.Stat_t, path string) (Device, error) {
	if st.Mode&unix.S_IFMT != unix.S_IFCHR {
		return Device{}, fmt.Errorf("session: %s is not a character device", path)
	}
	return Device{Major: unix.Major(st.Rdev), Minor: unix.Minor(st.Rdev)}, nil
}

type Listener interface {
	DevicePaused(d Device) error
	DeviceResumed(d Device, fd int) error
	ActiveChanged(active bool) error
	PrepareForSleep(sleeping bool) error
}

type Logind struct {
	conn       *dbus.Conn
	manager    logind.Manager
	session    logind.Session
	seat       logind.Seat
	listener   Listener
	loop       *loop.Loop
	watch      *logind.Watch
	unwatch    context.CancelFunc
	active     bool
	sleeping   atomic.Bool
	work       loop.Serial
	deferPause bool
}

func SystemBus(getenv func(string) string) string {
	if a := getenv("DBUS_SYSTEM_BUS_ADDRESS"); a != "" {
		return a
	}
	return defaultSystem
}

func Open(l *loop.Loop, address string, getenv func(string) string, listener Listener) (lg *Logind, err error) {
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("session: connect to the system bus: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, conn.Close())
		}
	}()
	ctx := context.Background()
	lg = &Logind{conn: conn, manager: logind.NewManager(conn), listener: listener, loop: l}
	if lg.session, err = ownSession(ctx, lg.manager, getenv); err != nil {
		return nil, fmt.Errorf("session: find this process's logind session: %w", err)
	}
	if lg.seat, err = lg.session.Seat(ctx); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if lg.active, err = lg.session.Active(ctx); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if err := lg.session.TakeControl(ctx, false); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	var watching context.Context
	watching, lg.unwatch = context.WithCancel(ctx)
	if lg.watch, err = lg.session.Watch(watching, lg.relay); err != nil {
		lg.unwatch()
		return nil, fmt.Errorf("session: %w", errors.Join(err, lg.session.ReleaseControl(ctx)))
	}
	return lg, nil
}

func ownSession(ctx context.Context, m logind.Manager, getenv func(string) string) (logind.Session, error) {
	if id := getenv("XDG_SESSION_ID"); id != "" {
		return m.GetSession(ctx, logind.SessionID(id))
	}
	pid, err := intconv.Exact[uint32](os.Getpid())
	if err != nil {
		return logind.Session{}, fmt.Errorf("pid %d: %w", os.Getpid(), err)
	}
	return m.GetSessionByPID(ctx, pid)
}

func (lg *Logind) Seat() string {
	return string(lg.seat.ID())
}

func (lg *Logind) Active() bool {
	return lg.active
}

func (lg *Logind) relay(e logind.Event, decodeErr error) {
	posted := lg.loop.Post(func() error { return errors.Join(decodeErr, lg.handle(e)) })
	if posted == nil {
		return
	}
	lg.unwatch()
	if r, ok := e.(logind.DeviceResumed); ok && errors.Is(posted, loop.ErrClosed) {
		_ = unix.Close(int(r.FD))
	}
}

func (lg *Logind) handle(e logind.Event) error {
	switch e := e.(type) {
	case logind.DevicePaused:
		err := lg.listener.DevicePaused(e.Device())
		if e.Kind == logind.PausePause && !lg.deferPause {
			lg.work.Run(func() { lg.completePause(e.Device()) })
		}
		return err
	case logind.DeviceResumed:
		return lg.listener.DeviceResumed(e.Device(), int(e.FD))
	case logind.Sleep:
		lg.sleeping.Store(e.Start)
		return lg.listener.PrepareForSleep(e.Start)
	case logind.ActiveChanged:
		lg.active = e.Active
		return lg.listener.ActiveChanged(e.Active)
	}
	return nil
}

func (lg *Logind) TakeDevice(path string) (int, Device, error) {
	d, err := DeviceOf(path)
	if err != nil {
		return -1, Device{}, err
	}
	fd, _, err := lg.session.TakeDevice(context.Background(), d)
	if err != nil {
		return -1, Device{}, fmt.Errorf("session: take %s: %w", path, err)
	}
	return fd, d, nil
}

func (lg *Logind) DeferPauses() {
	lg.deferPause = true
}

func (lg *Logind) CompletePause(d Device) error {
	if err := lg.session.PauseDeviceComplete(context.Background(), d); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

func (lg *Logind) completePause(d Device) {
	err := lg.CompletePause(d)
	if err == nil {
		return
	}
	if posted := lg.loop.Post(func() error { return err }); posted != nil && !errors.Is(posted, loop.ErrClosed) {
		panic(fmt.Sprintf("session: hand a pause failure back to the loop: %v", posted))
	}
}

func (lg *Logind) ReleaseDevice(d Device) error {
	if err := lg.session.ReleaseDevice(context.Background(), d); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

func (lg *Logind) InhibitLidSwitch() (*os.File, error) {
	f, err := lg.manager.Inhibit(context.Background(), logind.InhibitLidSwitch, "lys-wm", "lys-wm applies the lid policy itself", logind.InhibitBlock)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return f, nil
}

func (lg *Logind) LidClosed() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), logind.CallTimeout)
	defer cancel()
	var v dbus.Variant
	if err := lg.conn.Object(logind.BusName, logind.ManagerPath).CallWithContext(ctx, dbusx.PropertiesInterface+".Get", 0, logind.ManagerInterface, "LidClosed").Store(&v); err != nil {
		return false, fmt.Errorf("session: read logind's Manager.LidClosed: %w", err)
	}
	var closed bool
	if err := v.Store(&closed); err != nil {
		return false, fmt.Errorf("session: decode logind's Manager.LidClosed %s: %w", v.Signature(), err)
	}
	return closed, nil
}

func (lg *Logind) Suspend() error {
	if lg.sleeping.Load() {
		return nil
	}
	err := lg.manager.Power(context.Background(), logind.Suspend)
	var refusal dbus.Error
	switch {
	case errors.As(err, &refusal):
		return fmt.Errorf("session: logind refused to suspend: %s: %w", refusal.Name, err)
	case err != nil:
		return fmt.Errorf("session: suspend: %w", err)
	}
	lg.sleeping.Store(true)
	return nil
}

func (lg *Logind) SwitchTo(vt uint32) error {
	if err := lg.seat.SwitchTo(context.Background(), vt); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	return nil
}

func (lg *Logind) Close() error {
	lg.work.Wait()
	return errors.Join(lg.watch.Stop(), lg.session.ReleaseControl(context.Background()), lg.conn.Close())
}
