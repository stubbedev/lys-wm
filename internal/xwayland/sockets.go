package xwayland

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/server"
)

const maxDisplays = 32

type sockets struct {
	n        int
	lockPath string
	path     string
	fds      [2]int
}

func bindDisplay(dir string, first int, remove func(string) error) (*sockets, []error, error) {
	socketDir := filepath.Join(dir, ".X11-unix")
	switch err := os.Mkdir(socketDir, 0o700); {
	case errors.Is(err, os.ErrExist):
	case err != nil:
		return nil, nil, fmt.Errorf("xwayland: make %s: %w", socketDir, err)
	default:
		if err := os.Chmod(socketDir, 0o777|os.ModeSticky); err != nil {
			return nil, nil, fmt.Errorf("xwayland: open %s to every X server: %w", socketDir, err)
		}
	}
	var skipped []error
	for n := first; n < maxDisplays; n++ {
		s, err := claimDisplay(n, filepath.Join(dir, fmt.Sprintf(".X%d-lock", n)), filepath.Join(socketDir, fmt.Sprintf("X%d", n)), remove)
		if _, ok := errors.AsType[*foreignError](err); ok {
			skipped = append(skipped, fmt.Errorf(":%d %w", n, err))
			continue
		}
		if errors.Is(err, unix.EADDRINUSE) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if s != nil {
			return s, skipped, nil
		}
	}
	return nil, nil, errors.Join(append([]error{fmt.Errorf("xwayland: displays :0 to :%d in %s are all in use", maxDisplays-1, dir)}, skipped...)...)
}

func claimDisplay(n int, lock, path string, remove func(string) error) (*sockets, error) {
	taken, err := takeLock(lock, remove)
	if err != nil || !taken {
		return nil, err
	}
	if err := removeStale(path, remove); err != nil {
		if released := remove(lock); released != nil {
			return nil, fmt.Errorf("xwayland: release %s after %v: %w", lock, err, released)
		}
		return nil, err
	}
	s := &sockets{n: n, lockPath: lock, path: path, fds: [2]int{-1, -1}}
	if err := s.listen(); err != nil {
		return nil, errors.Join(err, s.close())
	}
	return s, nil
}

type foreignError struct {
	err error
}

func (e *foreignError) Error() string {
	return "is another user's: " + e.err.Error()
}

func (e *foreignError) Unwrap() error {
	return e.err
}

func removeStale(path string, remove func(string) error) error {
	switch err := remove(path); {
	case err == nil, errors.Is(err, os.ErrNotExist):
		return nil
	case errors.Is(err, os.ErrPermission):
		return &foreignError{err: err}
	default:
		return fmt.Errorf("xwayland: remove stale %s: %w", path, err)
	}
}

func takeLock(lock string, remove func(string) error) (bool, error) {
	for range 2 {
		f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444) //nolint:gosec // other X servers read the lock to see the display is taken
		if err == nil {
			_, err = fmt.Fprintf(f, "%10d\n", os.Getpid())
			if err = errors.Join(err, f.Close()); err != nil {
				return false, errors.Join(fmt.Errorf("xwayland: write %s: %w", lock, err), os.Remove(lock))
			}
			return true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return false, fmt.Errorf("xwayland: create %s: %w", lock, err)
		}
		if live, err := held(lock); err != nil || live {
			return false, err
		}
		if err := removeStale(lock, remove); err != nil {
			return false, err
		}
	}
	return false, nil
}

func held(lock string) (bool, error) {
	data, err := os.ReadFile(lock) //nolint:gosec // the lock path is built from the display number
	if errors.Is(err, os.ErrPermission) {
		return false, &foreignError{err: err}
	}
	if err != nil {
		return false, fmt.Errorf("xwayland: read %s: %w", lock, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return true, nil
	}
	return !errors.Is(unix.Kill(pid, 0), unix.ESRCH), nil
}

func (s *sockets) listen() error {
	for i, name := range []string{"@" + s.path, s.path} {
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("xwayland: socket for %s: %w", name, err)
		}
		s.fds[i] = fd
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: name}); err != nil {
			return fmt.Errorf("xwayland: bind %s: %w", name, err)
		}
		if err := unix.Listen(fd, 1); err != nil {
			return fmt.Errorf("xwayland: listen on %s: %w", name, err)
		}
	}
	return nil
}

func (s *sockets) close() error {
	var errs []error
	for i, fd := range s.fds {
		if fd >= 0 {
			errs = append(errs, unix.Close(fd))
			s.fds[i] = -1
		}
	}
	for _, p := range []string{s.path, s.lockPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("xwayland: release display :%d: %w", s.n, err)
	}
	return nil
}

type SocketState struct {
	N        int
	LockPath string
	Path     string
	FDs      [2]int
}

func (s *Server) Sockets(t *handover.FDs) SocketState {
	return SocketState{N: s.socks.n, LockPath: s.socks.lockPath, Path: s.socks.path, FDs: [2]int{t.Add(s.socks.fds[0]), t.Add(s.socks.fds[1])}}
}

func Adopt(l *loop.Loop, d *server.Display, opts Options, st SocketState, t *handover.FDs) (*Server, error) {
	binary, err := exec.LookPath(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("xwayland: find %s (set %s to its path): %w", opts.Binary, Env, err)
	}
	opts.Binary = binary
	socks := &sockets{n: st.N, lockPath: st.LockPath, path: st.Path}
	for i, n := range st.FDs {
		if socks.fds[i], err = t.Get(n); err != nil {
			return nil, err
		}
	}
	s := &Server{loop: l, display: d, opts: opts, socks: socks}
	if s.shell, err = newShell(d, s.IsClient, s.associated, s.dissociated); err != nil {
		return nil, errors.Join(err, socks.close())
	}
	if err := s.arm(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}
