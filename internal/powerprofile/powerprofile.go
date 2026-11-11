// Package powerprofile follows power-profiles-daemon's active profile on
// the system bus, so lys-wm can save power while the user asked for it.
package powerprofile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/third_party/godbus"

	"github.com/stubbedev/lys-wm/internal/loop"
)

const (
	propsIface    = "org.freedesktop.DBus.Properties"
	activeProfile = "ActiveProfile"
	powerSaver    = "power-saver"
	callTimeout   = 5 * time.Second
)

type service struct {
	name string
	path dbus.ObjectPath
}

var services = [...]service{
	{"org.freedesktop.UPower.PowerProfiles", "/org/freedesktop/UPower/PowerProfiles"},
	{"net.hadess.PowerProfiles", "/net/hadess/PowerProfiles"},
}

type profile struct {
	asOf  dbus.Sequence
	owned bool
	saver bool
}

type Watcher struct {
	conn    *dbus.Conn
	loop    *loop.Loop
	changed func(saver bool) error
	report  func(error)
	cancel  context.CancelFunc
	watches []*dbusx.Watch
	read    chan struct{}

	mu   sync.Mutex
	lost []error

	profiles [len(services)]profile
	saver    bool
}

func Watch(l *loop.Loop, address string, changed func(saver bool) error, report func(error)) (*Watcher, error) {
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("powerprofile: connect to the system bus: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{conn: conn, loop: l, changed: changed, report: report, cancel: cancel, watches: make([]*dbusx.Watch, 0, len(services)), read: make(chan struct{})}
	for i, s := range services {
		watch, err := dbusx.WatchSignals(ctx, conn, dbusx.FromOwner(s.name), w.failed,
			dbusx.OnOwnerChange(s.name, func(_, owner string, announced dbus.Sequence) error {
				if owner == "" {
					return w.post(i, profile{asOf: announced})
				}
				return w.refresh(ctx, i)
			}),
			dbusx.On(dbusx.PropertiesChanged, s.path, func(c dbusx.ChangedProperties, sent dbus.Sequence) error {
				return w.signalled(i, c, sent)
			}).WithArg0(s.name))
		if err != nil {
			close(w.read)
			return nil, errors.Join(fmt.Errorf("powerprofile: follow %s: %w", s.name, err), w.Close())
		}
		w.watches = append(w.watches, watch)
	}
	go func() {
		defer close(w.read)
		for i := range services {
			if err := w.refresh(ctx, i); err != nil {
				w.failed(err)
			}
		}
	}()
	return w, nil
}

func (w *Watcher) signalled(i int, c dbusx.ChangedProperties, sent dbus.Sequence) error {
	v, ok := c.Changed[activeProfile]
	err := dbus.CloseFDs([]any{c.Changed})
	if !ok {
		return err
	}
	saver, perr := isSaver(services[i], v)
	if perr != nil {
		return errors.Join(perr, err)
	}
	return errors.Join(w.post(i, profile{asOf: sent, owned: true, saver: saver}), err)
}

func isSaver(s service, v dbus.Variant) (bool, error) {
	p, ok := v.Value().(string)
	if !ok {
		return false, fmt.Errorf("powerprofile: %s.%s is a %s, not a string", s.name, activeProfile, v.Signature())
	}
	return p == powerSaver, nil
}

func (w *Watcher) refresh(ctx context.Context, i int) error {
	p, err := read(ctx, w.conn, services[i])
	if err != nil {
		return err
	}
	return w.post(i, p)
}

func read(ctx context.Context, conn *dbus.Conn, s service) (profile, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	has := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, s.name)
	var owned bool
	if err := has.Store(&owned); err != nil {
		return profile{}, fmt.Errorf("powerprofile: ask whether %s runs: %w", s.name, err)
	}
	if !owned {
		return profile{asOf: has.ResponseSequence}, nil
	}
	get := conn.Object(s.name, s.path).CallWithContext(ctx, propsIface+".Get", 0, s.name, activeProfile)
	var v dbus.Variant
	if err := get.Store(&v); err != nil {
		return profile{}, fmt.Errorf("powerprofile: read %s.%s: %w", s.name, activeProfile, err)
	}
	saver, err := isSaver(s, v)
	if err != nil {
		return profile{}, err
	}
	return profile{asOf: get.ResponseSequence, owned: true, saver: saver}, nil
}

func (w *Watcher) post(i int, p profile) error {
	if err := w.loop.Post(func() error { return w.apply(i, p) }); err != nil {
		return fmt.Errorf("powerprofile: hand %s's profile to the loop: %w", services[i].name, err)
	}
	return nil
}

func (w *Watcher) apply(i int, p profile) error {
	if p.asOf <= w.profiles[i].asOf {
		return nil
	}
	w.profiles[i] = p
	saver := false
	for _, q := range w.profiles {
		if q.owned {
			saver = q.saver
			break
		}
	}
	if saver == w.saver {
		return nil
	}
	w.saver = saver
	return w.changed(saver)
}

func (w *Watcher) failed(err error) {
	if perr := w.loop.Post(func() error {
		w.report(err)
		return nil
	}); perr != nil {
		w.mu.Lock()
		w.lost = append(w.lost, fmt.Errorf("powerprofile: report %w to the loop: %w", err, perr))
		w.mu.Unlock()
	}
}

func (w *Watcher) Close() error {
	w.cancel()
	<-w.read
	var errs []error
	for _, watch := range w.watches {
		if err := watch.Stop(); err != nil && !errors.Is(err, dbusx.ErrDisconnected) {
			errs = append(errs, fmt.Errorf("powerprofile: %w", err))
		}
	}
	errs = append(errs, w.conn.Close())
	w.mu.Lock()
	defer w.mu.Unlock()
	return errors.Join(append(errs, w.lost...)...)
}
