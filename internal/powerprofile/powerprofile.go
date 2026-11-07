// Package powerprofile follows power-profiles-daemon's active profile on
// the system bus, so lys-wm can save power while the user asked for it.
package powerprofile

import (
	"context"
	"errors"
	"fmt"
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

type Watcher struct {
	conn    *dbus.Conn
	loop    *loop.Loop
	changed func(saver bool) error
	report  func(error)
	signals chan *dbus.Signal
	done    chan struct{}
	relayed chan struct{}
	saver   bool
}

func Watch(l *loop.Loop, address string, changed func(saver bool) error, report func(error)) (*Watcher, error) {
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("powerprofile: connect to the system bus: %w", err)
	}
	w := &Watcher{conn: conn, loop: l, changed: changed, report: report, signals: make(chan *dbus.Signal, 16), done: make(chan struct{}), relayed: make(chan struct{})}
	for _, s := range services {
		for _, match := range [][]dbus.MatchOption{
			dbusx.OwnerMatch(s.name),
			{dbus.WithMatchObjectPath(s.path), dbus.WithMatchInterface(propsIface), dbus.WithMatchMember("PropertiesChanged")},
		} {
			if err := conn.AddMatchSignal(match...); err != nil {
				return nil, errors.Join(fmt.Errorf("powerprofile: subscribe to %s: %w", s.name, err), conn.Close())
			}
		}
	}
	conn.Signal(w.signals)
	go w.relay()
	return w, nil
}

func (w *Watcher) relay() {
	defer close(w.relayed)
	saver, err := w.read()
	if w.post(saver, err) != nil {
		return
	}
	for {
		select {
		case <-w.done:
			return
		case sig, ok := <-w.signals:
			if !ok {
				return
			}
			saver, known, err := w.signalled(sig)
			if known && w.post(saver, err) != nil {
				return
			}
		}
	}
}

func (w *Watcher) signalled(sig *dbus.Signal) (saver, known bool, err error) {
	for _, s := range services {
		if _, _, ok := dbusx.NameOwnerChange(sig, s.name); ok {
			saver, err := w.read()
			return saver, true, err
		}
	}
	if sig.Name != propsIface+".PropertiesChanged" || len(sig.Body) < 2 || !ours(sig) {
		return false, false, nil
	}
	props, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return false, false, nil
	}
	v, ok := props[activeProfile]
	if !ok {
		return false, false, nil
	}
	profile, ok := v.Value().(string)
	if !ok {
		return false, true, fmt.Errorf("powerprofile: %s changed to a %s, not a string", activeProfile, v.Signature())
	}
	return profile == powerSaver, true, nil
}

func ours(sig *dbus.Signal) bool {
	iface, _ := sig.Body[0].(string)
	for _, s := range services {
		if sig.Path == s.path && iface == s.name {
			return true
		}
	}
	return false
}

func (w *Watcher) read() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	for _, s := range services {
		var owned bool
		if err := w.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, s.name).Store(&owned); err != nil {
			return false, fmt.Errorf("powerprofile: ask whether %s runs: %w", s.name, err)
		}
		if !owned {
			continue
		}
		var v dbus.Variant
		if err := w.conn.Object(s.name, s.path).CallWithContext(ctx, propsIface+".Get", 0, s.name, activeProfile).Store(&v); err != nil {
			return false, fmt.Errorf("powerprofile: read %s.%s: %w", s.name, activeProfile, err)
		}
		profile, ok := v.Value().(string)
		if !ok {
			return false, fmt.Errorf("powerprofile: %s.%s is a %s, not a string", s.name, activeProfile, v.Signature())
		}
		return profile == powerSaver, nil
	}
	return false, nil
}

func (w *Watcher) post(saver bool, err error) error {
	return w.loop.Post(func() error {
		if err != nil {
			w.report(err)
			return nil
		}
		if saver == w.saver {
			return nil
		}
		w.saver = saver
		return w.changed(saver)
	})
}

func (w *Watcher) Close() error {
	close(w.done)
	<-w.relayed
	w.conn.RemoveSignal(w.signals)
	return w.conn.Close()
}
