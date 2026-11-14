package spawn

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/fdmsg"
)

func init() {
	value, ok := os.LookupEnv(spawnerEnv)
	if !ok {
		return
	}
	if err := serve(value); err != nil {
		fmt.Fprintln(os.Stderr, "lys-wm: spawner:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func serve(value string) error {
	runtime.LockOSThread()
	fd, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s=%q names no fd: %w", spawnerEnv, value, err)
	}
	if err := os.Unsetenv(spawnerEnv); err != nil {
		return fmt.Errorf("unset %s: %w", spawnerEnv, err)
	}
	unix.CloseOnExec(fd)
	name, err := unix.BytePtrFromString(spawnerName)
	if err != nil {
		return fmt.Errorf("name this process %s: %w", spawnerName, err)
	}
	if err := unix.Prctl(unix.PR_SET_NAME, uintptr(unsafe.Pointer(name)), 0, 0, 0); err != nil { //nolint:gosec // the kernel reads the name during the call
		return fmt.Errorf("name this process %s: %w", spawnerName, err)
	}
	signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	r := fdmsg.NewReceiver(fd, maxMessage, maxFDs)
	for {
		req, fds, err := fdmsg.Recv[request](r)
		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := start(fd, req, fds); errors.Is(err, unix.EPIPE) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func start(sock int, req request, fds []int) error {
	files := make([]*os.File, len(fds))
	for i, fd := range fds {
		files[i] = os.NewFile(uintptr(fd), "spawned "+strconv.Itoa(i))
	}
	p, err := launch(req, files)
	for _, f := range files {
		_ = f.Close()
	}
	if err != nil {
		return fdmsg.Send(sock, event{ID: req.ID, Kind: eventRefused, Err: err.Error()})
	}
	pidfd, err := unix.PidfdOpen(p.Pid, 0)
	if err != nil {
		return errors.Join(fdmsg.Send(sock, event{ID: req.ID, Kind: eventRefused, Err: fmt.Sprintf("open a pidfd for pid %d: %v", p.Pid, err)}), p.Kill())
	}
	sent := fdmsg.Send(sock, event{ID: req.ID, Kind: eventStarted, PID: p.Pid}, pidfd)
	if err := errors.Join(sent, unix.Close(pidfd)); err != nil {
		return err
	}
	go report(sock, req.ID, p)
	return nil
}

func report(sock int, id uint64, p *os.Process) {
	err := reap(sock, id, p)
	if err == nil {
		return
	}
	if errors.Is(err, unix.EPIPE) {
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "lys-wm: spawner:", err)
	os.Exit(1)
}

func reap(sock int, id uint64, p *os.Process) error {
	state, err := p.Wait()
	if err != nil {
		return fmt.Errorf("wait for pid %d: %w", p.Pid, err)
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return fmt.Errorf("pid %d exited with a %T, not a wait status", p.Pid, state.Sys())
	}
	return fdmsg.Send(sock, event{ID: id, Kind: eventExited, Status: status})
}

func launch(req request, files []*os.File) (*os.Process, error) {
	if len(files) < stdFiles {
		return nil, fmt.Errorf("a request for %s passed %d fds, fewer than stdin, stdout and stderr", req.Path, len(files))
	}
	env := req.Env
	if env == nil {
		env = []string{}
	}
	p, err := os.StartProcess(req.Path, req.Args, &os.ProcAttr{
		Env:   env,
		Files: files,
		Sys:   &syscall.SysProcAttr{Setsid: req.Setsid, Pdeathsig: req.DeathSignal},
	})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", req.Path, err)
	}
	return p, nil
}
