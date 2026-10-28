// Package systemd hands the session's environment to D-Bus activation and
// the systemd user manager, and runs lys-wm-session.target while lys-wm runs.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	Target      = "lys-wm-session.target"
	callTimeout = 5 * time.Second
	manager     = "org.freedesktop.systemd1.Manager"
)

type Session struct {
	conn    *dbus.Conn
	absent  []string
	restore []string
}

func call(obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	c := obj.CallWithContext(ctx, method, 0, args...)
	if c.Err != nil {
		c.Err = fmt.Errorf("systemd: %s: %w", method, c.Err)
	}
	return c
}

func (s *Session) manager() dbus.BusObject {
	return s.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
}

func (s *Session) environment() (map[string]string, error) {
	var assignments []string
	if err := call(s.manager(), "org.freedesktop.DBus.Properties.Get", manager, "Environment").Store(&assignments); err != nil {
		return nil, fmt.Errorf("systemd: read the user manager's environment: %w", err)
	}
	env := make(map[string]string, len(assignments))
	for _, a := range assignments {
		k, v, _ := strings.Cut(a, "=")
		env[k] = v
	}
	return env, nil
}

func (s *Session) undo() error {
	return call(s.manager(), manager+".UnsetAndSetEnvironment", s.absent, s.restore).Err
}

func Start(address string, env [][2]string) (*Session, error) {
	if address == "" {
		return nil, errors.New("systemd: DBUS_SESSION_BUS_ADDRESS is not set")
	}
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("systemd: connect to the session bus: %w", err)
	}
	s := &Session{conn: conn}
	prior, err := s.environment()
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	activation := make(map[string]string, len(env))
	assignments := make([]string, len(env))
	for i, kv := range env {
		activation[kv[0]] = kv[1]
		assignments[i] = kv[0] + "=" + kv[1]
		if v, ok := prior[kv[0]]; ok {
			s.restore = append(s.restore, kv[0]+"="+v)
		} else {
			s.absent = append(s.absent, kv[0])
		}
	}
	if err := errors.Join(
		call(conn.BusObject(), "org.freedesktop.DBus.UpdateActivationEnvironment", activation).Err,
		call(s.manager(), manager+".SetEnvironment", assignments).Err,
	); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if err := call(s.manager(), manager+".StartUnit", Target, "replace").Err; err != nil {
		return nil, errors.Join(err, s.undo(), conn.Close())
	}
	return s, nil
}

func (s *Session) Close() error {
	return errors.Join(
		call(s.manager(), manager+".StopUnit", Target, "replace").Err,
		s.undo(),
		s.conn.Close(),
	)
}
