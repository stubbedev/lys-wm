package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/handover"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/session"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

const (
	preflightLimit = 15 * time.Second
	quiesceLimit   = 5 * time.Second
	quiesceStep    = 5 * time.Millisecond
	restoreLimit   = 60 * time.Second
	stderrTail     = 4096
	stderrSettle   = time.Second
	stateReady     = "ready"
	stateCommit    = "commit"
	stateAck       = "ack"
)

var errNoHolder = errors.New("refused, nothing changed: this session started without --holder, so it cannot move to another binary. Add --holder to the command that starts lys-wm and log in again; until then, lys-wmctl reload reads the config and logging out moves to a new binary")

type preflightInfo struct {
	Format   int      `json:"format"`
	Config   string   `json:"config_error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type handoverState struct {
	Format     int
	Compositor *compositor.HandoverState
	IPC        int
	Reply      int
	X          *xwayland.SocketState
	Watchers   []int
	Stderr     int
}

type upgrader struct {
	loop    *loop.Loop
	comp    *compositor.Compositor
	display *server.Display
	srv     *ipc.Server
	xw      *xwayland.Server
	args    []string
	path    string
	holder  *session.Holder
	opened  opened
	held    []*os.File
	running bool
	dropX11 bool
}

func preflight(binary, configPath string) (preflightInfo, error) {
	var out, errOut bytes.Buffer
	cmd := exec.Command(binary, "--handover-info", "--config", configPath) //nolint:gosec // the path is the one the user named in lys-wmctl upgrade
	cmd.Stdout, cmd.Stderr = &out, &errOut
	timer := time.AfterFunc(preflightLimit, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	if err := cmd.Run(); err != nil {
		return preflightInfo{}, fmt.Errorf("%s does not answer as a lys-wm that can take over: %w: %s", binary, err, strings.TrimSpace(errOut.String()))
	}
	var info preflightInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		return preflightInfo{}, fmt.Errorf("%s answered %q, which is not a handover description: %w", binary, out.String(), err)
	}
	return info, nil
}

func (u *upgrader) refusal(binary string, info preflightInfo) error {
	if info.Format != handover.Format {
		return fmt.Errorf("%s hands over in format %d, this lys-wm in format %d; log out to move between them", binary, info.Format, handover.Format)
	}
	if info.Config != "" {
		return fmt.Errorf("%s rejects the config: %s", binary, info.Config)
	}
	why := u.comp.UpgradeBlockers(u.dropX11)
	if u.opened.native != nil && u.opened.native.paused() {
		why = append(why, "this session is not on the active VT")
	}
	for _, k := range u.display.Unkeepable(u.skipClient) {
		if k.PID == 0 {
			why = append(why, k.Reason)
			continue
		}
		why = append(why, fmt.Sprintf("the client of pid %d cannot be kept: %s", k.PID, k.Reason))
	}
	if u.xw != nil && u.xw.Running() && !u.dropX11 {
		why = append(why, "Xwayland is running and cannot be kept; quit the X11 programs and repeat with --drop-x11, which ends Xwayland and every X11 client")
	}
	if len(why) > 0 {
		return errors.New("refused, nothing changed: " + strings.Join(why, "; "))
	}
	return nil
}

func (u *upgrader) skipClient(c *server.Client) bool {
	return u.dropX11 && u.xw != nil && u.xw.IsClient(c)
}

func (u *upgrader) start(binary string, dropX11 bool, conn *os.File, done func(ipc.UpgradeResult, error)) {
	if u.holder == nil {
		done(ipc.UpgradeResult{}, errNoHolder)
		return
	}
	if u.running {
		done(ipc.UpgradeResult{}, errors.New("an upgrade is already running"))
		return
	}
	if !filepath.IsAbs(binary) {
		done(ipc.UpgradeResult{}, fmt.Errorf("the binary %q is not an absolute path", binary))
		return
	}
	u.running, u.dropX11 = true, dropX11
	finish := func(err error) {
		u.running = false
		done(ipc.UpgradeResult{}, err)
	}
	go func() {
		info, err := preflight(binary, u.path)
		_ = u.loop.Post(func() error {
			if err == nil {
				err = u.refusal(binary, info)
			}
			if err != nil {
				finish(err)
				return nil
			}
			u.quiesce(binary, conn, finish)
			return nil
		})
	}()
}

func (u *upgrader) quiesce(binary string, conn *os.File, finish func(error)) {
	resume := func(cause error) {
		var unpark error
		if u.opened.native != nil {
			unpark = u.opened.native.unpark()
		}
		for _, f := range u.held {
			_ = f.Close()
		}
		u.held = nil
		finish(errors.Join(cause, unpark, u.srv.ResumeAccept(), u.display.Resume(), u.comp.Resume()))
	}
	if err := errors.Join(u.srv.PauseAccept(), u.display.Park(), u.comp.Settle()); err != nil {
		resume(fmt.Errorf("stop for the handover: %w", err))
		return
	}
	native := u.opened.native
	if native != nil {
		if err := native.park(); err != nil {
			resume(fmt.Errorf("stop for the handover: %w", err))
			return
		}
	}
	deadline := time.Now().Add(quiesceLimit)
	var timer *loop.Timer
	var err error
	timer, err = u.loop.AddTimer(func() error {
		why := u.comp.Busy()
		if why == "" {
			u.comp.Park()
		}
		if why == "" && native != nil {
			why = native.busy()
		}
		if why == "" {
			if err := timer.Remove(); err != nil {
				return err
			}
			u.spawn(binary, conn, resume)
			return nil
		}
		if time.Now().After(deadline) {
			resume(fmt.Errorf("refused, nothing changed: %s for over %v", why, quiesceLimit))
			return timer.Remove()
		}
		return timer.Arm(quiesceStep)
	})
	if err != nil {
		resume(err)
		return
	}
	if err := timer.Arm(quiesceStep); err != nil {
		resume(err)
	}
}

func (u *upgrader) state(conn *os.File, t *handover.FDs) (*handoverState, error) {
	cs, err := u.comp.HandoverState(t, u.skipClient)
	if err != nil {
		return nil, err
	}
	ln, err := u.srv.ListenerFD()
	if err != nil {
		return nil, fmt.Errorf("the IPC listener: %w", err)
	}
	u.held = append(u.held, ln)
	st := &handoverState{Format: handover.Format, Compositor: cs, IPC: t.Add(int(ln.Fd())), Reply: t.Add(int(conn.Fd())), Stderr: t.Add(int(os.Stderr.Fd()))}
	if u.xw != nil {
		x := u.xw.Sockets(t)
		st.X = &x
	}
	watchers, err := u.srv.WatcherFiles()
	if err != nil {
		return nil, err
	}
	for _, f := range watchers {
		st.Watchers = append(st.Watchers, t.Add(int(f.Fd())))
	}
	u.held = append(u.held, watchers...)
	return st, nil
}

type tail struct {
	mu     sync.Mutex
	buf    []byte
	closed chan struct{}
}

func (w *tail) settled() string {
	select {
	case <-w.closed:
	case <-time.After(stderrSettle):
	}
	return w.String()
}

func (w *tail) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > stderrTail {
		w.buf = w.buf[len(w.buf)-stderrTail:]
	}
	_, _ = os.Stderr.Write(p)
	return len(p), nil
}

func (w *tail) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.buf))
}

func (u *upgrader) spawn(binary string, conn *os.File, resume func(error)) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		resume(fmt.Errorf("socketpair for the handover: %w", err))
		return
	}
	var stderrPipe [2]int
	if err := unix.Pipe2(stderrPipe[:], unix.O_CLOEXEC); err != nil {
		resume(errors.Join(fmt.Errorf("pipe for the new lys-wm's stderr: %w", err), unix.Close(pair[0]), unix.Close(pair[1])))
		return
	}
	logs := &tail{closed: make(chan struct{})}
	go func() {
		defer close(logs.closed)
		f := os.NewFile(uintptr(stderrPipe[0]), "stderr")
		defer f.Close()
		_, _ = io.Copy(logs, f)
	}()
	t := &handover.FDs{}
	st, err := u.state(conn, t)
	if err != nil {
		resume(errors.Join(fmt.Errorf("describe the session: %w", err), unix.Close(pair[0]), unix.Close(pair[1]), unix.Close(stderrPipe[1])))
		return
	}
	pid, err := u.holder.Spawn(binary, u.args, pair[1], stderrPipe[1])
	err = errors.Join(err, unix.Close(pair[1]), unix.Close(stderrPipe[1]))
	if err != nil {
		resume(errors.Join(fmt.Errorf("start %s: %w", binary, err), unix.Close(pair[0])))
		return
	}
	go u.drive(pid, pair[0], st, t, logs, binary, resume)
}

func (u *upgrader) drive(pid int, fd int, st *handoverState, t *handover.FDs, logs *tail, binary string, resume func(error)) {
	fail := func(cause error) {
		_ = unix.Kill(pid, unix.SIGKILL)
		_ = unix.Close(fd)
		why := logs.settled()
		_ = u.loop.Post(func() error {
			resume(fmt.Errorf("%s did not take over and this lys-wm keeps running: %w: %s", binary, cause, why))
			return nil
		})
	}
	u.holder.OnExit(pid, func() { _ = unix.Shutdown(fd, unix.SHUT_RDWR) })
	timer := time.AfterFunc(restoreLimit, func() { _ = unix.Shutdown(fd, unix.SHUT_RDWR) })
	defer timer.Stop()
	err := handover.Send(fd, st, t)
	t.CloseOwned()
	if err != nil {
		fail(err)
		return
	}
	var ready string
	if _, err := handover.Receive(fd, &ready); err != nil {
		fail(fmt.Errorf("wait for it to be ready: %w", err))
		return
	}
	if ready != stateReady {
		fail(fmt.Errorf("it answered %q", ready))
		return
	}
	if err := handover.Send(fd, stateCommit, nil); err != nil {
		fail(err)
		return
	}
	var ack string
	if _, err := handover.Receive(fd, &ack); err != nil || ack != stateAck {
		fail(fmt.Errorf("wait for it to take the commit: %q, %w", ack, err))
		return
	}
	_ = u.loop.Post(u.leave)
}

func (u *upgrader) leave() error {
	defer os.Exit(0)
	defer func() { _ = recover() }()
	if u.opened.native != nil {
		return u.opened.native.retire()
	}
	return nil
}

func dialHolder(l *loop.Loop) (*session.Holder, error) {
	value := os.Getenv(holderEnv)
	if value == "" {
		return nil, nil
	}
	if err := os.Unsetenv(holderEnv); err != nil {
		return nil, fmt.Errorf("clear %s: %w", holderEnv, err)
	}
	fd, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", holderEnv, value, err)
	}
	unix.CloseOnExec(fd)
	return session.DialHolder(fd, l)
}

func holds(args []string, getenv func(string) string) bool {
	if getenv(holderEnv) != "" {
		return false
	}
	wanted := false
	for _, a := range args {
		switch strings.TrimLeft(a, "-") {
		case "check-config", "default-config", "config-schema", "handover-info", "h", "help":
			return false
		case "holder":
			wanted = true
		}
	}
	return wanted
}

type adoption struct {
	fd    int
	state *handoverState
	fds   *handover.FDs
}

func receiveHandover(fd int) (*adoption, error) {
	st := &handoverState{}
	t, err := handover.Receive(fd, st)
	if err != nil {
		return nil, fmt.Errorf("receive the session: %w", err)
	}
	if st.Format != handover.Format {
		return nil, fmt.Errorf("the session is in handover format %d, this lys-wm reads %d", st.Format, handover.Format)
	}
	return &adoption{fd: fd, state: st, fds: t}, nil
}

func (a *adoption) file(i int, name string) (*os.File, error) {
	fd, err := a.fds.Get(i)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

type adopted struct {
	a       *adoption
	loop    *loop.Loop
	comp    *compositor.Compositor
	srv     *ipc.Server
	holder  *session.Holder
	release func() error
	reply   *os.File
	binary  string
	stop    func(error)
}

func (a *adopted) begin() error {
	release, err := a.comp.AdoptHandover(a.a.state.Compositor, a.a.fds)
	if err != nil {
		return err
	}
	a.release = release
	if a.reply, err = a.a.file(a.a.state.Reply, "upgrade-reply"); err != nil {
		return err
	}
	return a.watch()
}

func (a *adopted) watch() error {
	var timer *loop.Timer
	var err error
	settled := false
	timer, err = a.loop.AddTimer(func() error {
		if !settled {
			if a.comp.Busy() != "" {
				return timer.Arm(quiesceStep)
			}
			if err := a.comp.FinishAdoption(); err != nil {
				return err
			}
			settled = true
		}
		if !a.comp.AdoptionShown() {
			return timer.Arm(quiesceStep)
		}
		if err := timer.Remove(); err != nil {
			return err
		}
		go a.handshake()
		return nil
	})
	if err != nil {
		return err
	}
	return timer.Arm(quiesceStep)
}

func (a *adopted) handshake() {
	abort := func(err error) {
		_ = a.loop.Post(func() error {
			a.stop(fmt.Errorf("the handover: %w", err))
			return nil
		})
	}
	if err := handover.Send(a.a.fd, stateReady, nil); err != nil {
		abort(err)
		return
	}
	var commit string
	if _, err := handover.Receive(a.a.fd, &commit); err != nil || commit != stateCommit {
		abort(fmt.Errorf("wait for the commit: %q, %w", commit, err))
		return
	}
	if err := a.holder.Commit(); err != nil {
		abort(fmt.Errorf("tell the holder this process took over: %w", err))
		return
	}
	stderr, err := a.a.fds.Get(a.a.state.Stderr)
	if err != nil {
		abort(err)
		return
	}
	if err := unix.Dup3(stderr, int(os.Stderr.Fd()), 0); err != nil {
		abort(fmt.Errorf("keep the session's stderr: %w", err))
		return
	}
	if err := handover.Send(a.a.fd, stateAck, nil); err != nil {
		abort(err)
		return
	}
	_ = a.loop.Post(a.commit)
}

func (a *adopted) commit() error {
	if err := a.release(); err != nil {
		return fmt.Errorf("release the clients: %w", err)
	}
	if err := a.comp.CompleteAdoption(); err != nil {
		return err
	}
	a.srv.Start()
	for _, n := range a.a.state.Watchers {
		f, err := a.a.file(n, "ipc-watcher")
		if err != nil {
			return err
		}
		if err := a.srv.AdoptWatcher(f); err != nil {
			return err
		}
	}
	snap := a.comp.Snapshot()
	err := ipc.ReplyUpgrade(a.reply, ipc.UpgradeResult{PID: os.Getpid(), Binary: a.binary, Clients: len(a.a.state.Compositor.Display.Clients), Windows: len(snap.Windows)})
	return errors.Join(err, a.reply.Close(), unix.Close(a.a.fd))
}
