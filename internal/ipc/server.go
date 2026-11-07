package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/wm"
)

const (
	SocketEnv     = "LYS_WM_SOCKET"
	maxRequest    = 64 << 10
	ioTimeout     = 5 * time.Second
	acceptBackoff = 100 * time.Millisecond
	watchBacklog  = 256
)

var ErrShutdown = errors.New("ipc: lys-wm is shutting down")

func SocketPath(runtimeDir, display string) (string, error) {
	if !filepath.IsAbs(runtimeDir) {
		return "", fmt.Errorf("ipc: XDG_RUNTIME_DIR %q is not an absolute path", runtimeDir)
	}
	return filepath.Join(runtimeDir, "lys-wm", display+".sock"), nil
}

type Handler interface {
	Snapshot() Snapshot
	DispatchPrepared(a wm.Action, loaded func() error) error
	PrepareDispatch(a wm.Action) func() error
	Icon(w wm.WindowID, size int) (*image.RGBA, int, error)
}

type Upgrader func(binary string, dropX11 bool, conn *os.File, done func(UpgradeResult, error))

type watcher struct {
	lines chan []byte
}

type Server struct {
	loop    *loop.Loop
	handler Handler
	report  func(error)
	ln      *net.UnixListener
	done    chan struct{}
	closing sync.Once
	wg      sync.WaitGroup

	mu       sync.Mutex
	conns    map[*net.UnixConn]struct{}
	watching map[*net.UnixConn]struct{}

	upgrade  Upgrader
	resume   chan struct{}
	watchers []*watcher
	last     Snapshot
	pending  []Event
	changed  bool
}

func Listen(l *loop.Loop, path string, h Handler, report func(error)) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("ipc: create the socket directory: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("ipc: remove the stale socket: %w", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("ipc: listen: %w", err)
	}
	ln.SetUnlinkOnClose(true)
	s := &Server{loop: l, handler: h, report: report, ln: ln, done: make(chan struct{}), conns: map[*net.UnixConn]struct{}{}, watching: map[*net.UnixConn]struct{}{}}
	l.BeforeWait(s.publish)
	s.Start()
	return s, nil
}

func (s *Server) Close() error {
	var err error
	s.closing.Do(func() {
		close(s.done)
		err = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.CloseRead()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	if err != nil {
		return fmt.Errorf("ipc: close the listener: %w", err)
	}
	return nil
}

func (s *Server) shut() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Server) post(f func()) {
	_ = s.loop.Post(func() error {
		f()
		return nil
	})
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.AcceptUnix()
		if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
			s.waitResume()
			continue
		}
		if s.shut() {
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		if err != nil {
			s.post(func() { s.report(fmt.Errorf("ipc: accept: %w", err)) })
			time.Sleep(acceptBackoff)
			continue
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(conn) })
	}
}

func (s *Server) PauseAccept() error {
	s.mu.Lock()
	s.resume = make(chan struct{})
	s.mu.Unlock()
	if err := s.ln.SetDeadline(time.Now()); err != nil {
		return fmt.Errorf("ipc: stop accepting: %w", err)
	}
	return nil
}

func (s *Server) ResumeAccept() error {
	err := s.ln.SetDeadline(time.Time{})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resume != nil {
		close(s.resume)
		s.resume = nil
	}
	if err != nil {
		return fmt.Errorf("ipc: accept again: %w", err)
	}
	return nil
}

func (s *Server) waitResume() {
	s.mu.Lock()
	resume := s.resume
	s.mu.Unlock()
	if resume == nil {
		return
	}
	select {
	case <-resume:
	case <-s.done:
	}
}

func (s *Server) OnUpgrade(u Upgrader) {
	s.upgrade = u
}

func (s *Server) runUpgrade(conn *net.UnixConn, binary string, dropX11 bool) {
	file, err := conn.File()
	if err != nil {
		s.reply(conn, nil, fmt.Errorf("ipc: keep the upgrade's connection: %w", err))
		return
	}
	defer file.Close()
	type outcome struct {
		result UpgradeResult
		err    error
	}
	ch := make(chan outcome, 1)
	if _, err := onLoop(s, func() (struct{}, error) {
		if s.upgrade == nil {
			return struct{}{}, errors.New("this lys-wm cannot upgrade")
		}
		s.upgrade(binary, dropX11, file, func(r UpgradeResult, err error) { ch <- outcome{r, err} })
		return struct{}{}, nil
	}); err != nil {
		s.reply(conn, nil, err)
		return
	}
	select {
	case o := <-ch:
		s.reply(conn, o.result, o.err)
	case <-s.done:
	}
}

func ReplyUpgrade(file *os.File, result UpgradeResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("ipc: encode the upgrade's result: %w", err)
	}
	line, err := json.Marshal(Response{Version: Version, Result: body})
	if err != nil {
		return fmt.Errorf("ipc: encode the upgrade's response: %w", err)
	}
	_, err = file.Write(append(line, '\n'))
	return err
}

func (s *Server) ListenerFD() (*os.File, error) {
	return s.ln.File()
}

func (s *Server) Path() string {
	return s.ln.Addr().String()
}

func Adopt(l *loop.Loop, file *os.File, h Handler, report func(error)) (*Server, error) {
	fl, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("ipc: adopt the listener: %w", err)
	}
	ln, ok := fl.(*net.UnixListener)
	if !ok {
		return nil, errors.Join(fmt.Errorf("ipc: adopted a %T listener", fl), fl.Close())
	}
	ln.SetUnlinkOnClose(true)
	s := &Server{loop: l, handler: h, report: report, ln: ln, done: make(chan struct{}), conns: map[*net.UnixConn]struct{}{}, watching: map[*net.UnixConn]struct{}{}}
	l.BeforeWait(s.publish)
	return s, nil
}

func (s *Server) Start() {
	s.wg.Go(s.accept)
}

func (s *Server) forget(conn *net.UnixConn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	_ = conn.Close()
}

func readRequest(conn *net.UnixConn) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		return nil, err
	}
	line, err := bufio.NewReaderSize(conn, maxRequest).ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrBadRequest, maxRequest)
	case errors.Is(err, io.EOF) && len(line) > 0:
		return line, nil
	}
	return line, err
}

func write(conn *net.UnixConn, line []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	_, err := conn.Write(line)
	return err
}

func (s *Server) reply(conn *net.UnixConn, result any, failure error) {
	resp := Response{Version: Version}
	if failure != nil {
		resp.Error = failure.Error()
	} else if result != nil {
		body, err := json.Marshal(result)
		if err != nil {
			resp.Error = fmt.Sprintf("ipc: encode the result: %v", err)
		}
		resp.Result = body
	}
	line, err := json.Marshal(resp)
	if err != nil {
		s.post(func() { s.report(fmt.Errorf("ipc: encode a response: %w", err)) })
		return
	}
	_ = write(conn, append(line, '\n'))
}

func onLoop[T any](s *Server, f func() (T, error)) (T, error) {
	type outcome struct {
		v   T
		err error
	}
	ch := make(chan outcome, 1)
	var zero T
	if err := s.loop.Post(func() error {
		v, err := f()
		ch <- outcome{v, err}
		return nil
	}); err != nil {
		return zero, ErrShutdown
	}
	select {
	case o := <-ch:
		return o.v, o.err
	case <-s.done:
	}
	select {
	case o := <-ch:
		return o.v, o.err
	default:
		return zero, ErrShutdown
	}
}

func (s *Server) serve(conn *net.UnixConn) {
	defer s.forget(conn)
	line, err := readRequest(conn)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.reply(conn, nil, err)
		}
		return
	}
	req, err := parseRequest(line)
	if err != nil {
		s.reply(conn, nil, err)
		return
	}
	if req.kind == KindWatch {
		s.watch(conn, true)
		return
	}
	if req.kind == KindIcon {
		result, err := s.icon(req.icon)
		s.reply(conn, result, err)
		return
	}
	if req.kind == KindUpgrade {
		s.runUpgrade(conn, req.binary, req.dropX11)
		return
	}
	var loaded func() error
	if req.kind == KindDispatch {
		loaded = s.handler.PrepareDispatch(req.action)
	}
	result, err := onLoop(s, func() (any, error) {
		if req.kind == KindDispatch {
			return nil, s.handler.DispatchPrepared(req.action, loaded)
		}
		return s.handler.Snapshot().answer(req.kind), nil
	})
	s.reply(conn, result, err)
}

type iconPixels struct {
	img   *image.RGBA
	scale int
}

func (s *Server) icon(req iconRequest) (any, error) {
	px, err := onLoop(s, func() (iconPixels, error) {
		img, scale, err := s.handler.Icon(req.window, req.size)
		return iconPixels{img, scale}, err
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, px.img); err != nil {
		return nil, fmt.Errorf("ipc: encode the icon of window %d: %w", req.window, err)
	}
	return IconImage{Size: px.img.Bounds().Dx(), Scale: px.scale, PNG: buf.Bytes()}, nil
}

func (s *Server) WatcherFiles() ([]*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := make([]*os.File, 0, len(s.watching))
	for conn := range s.watching {
		f, err := conn.File()
		if err != nil {
			return nil, fmt.Errorf("ipc: keep a watcher: %w", err)
		}
		files = append(files, f)
	}
	return files, nil
}

func (s *Server) AdoptWatcher(file *os.File) error {
	fc, err := net.FileConn(file)
	if err != nil {
		return fmt.Errorf("ipc: adopt a watcher: %w", err)
	}
	conn, ok := fc.(*net.UnixConn)
	if !ok {
		return errors.Join(fmt.Errorf("ipc: adopted a %T watcher", fc), fc.Close())
	}
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	s.wg.Go(func() {
		defer s.forget(conn)
		s.watch(conn, false)
	})
	return nil
}

func (s *Server) watch(conn *net.UnixConn, greet bool) {
	s.mu.Lock()
	s.watching[conn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.watching, conn)
		s.mu.Unlock()
	}()
	w := &watcher{lines: make(chan []byte, watchBacklog)}
	if _, err := onLoop(s, func() (struct{}, error) {
		if len(s.watchers) == 0 {
			s.last, s.pending, s.changed = s.handler.Snapshot(), nil, false
		}
		s.watchers = append(s.watchers, w)
		return struct{}{}, nil
	}); err != nil {
		s.reply(conn, nil, err)
		return
	}
	defer s.post(func() { s.drop(w) })
	if greet {
		s.reply(conn, nil, nil)
	}
	for {
		select {
		case line, ok := <-w.lines:
			if !ok || write(conn, line) != nil {
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *Server) drop(w *watcher) {
	if i := slices.Index(s.watchers, w); i >= 0 {
		s.watchers = slices.Delete(s.watchers, i, i+1)
		close(w.lines)
	}
}

func (s *Server) Dispatched(a wm.Action) {
	if len(s.watchers) > 0 {
		s.Announce(DispatchEvent{Action: a.String()})
	}
}

func (s *Server) Announce(e Event) {
	if len(s.watchers) > 0 {
		s.pending = append(s.pending, e)
	}
}

func (s *Server) Changed() {
	s.changed = true
}

func (s *Server) publish() error {
	if len(s.watchers) == 0 || !s.changed && len(s.pending) == 0 || s.shut() {
		return nil
	}
	s.changed = false
	next := s.handler.Snapshot()
	events := append(s.pending, Diff(s.last, next)...)
	s.last, s.pending = next, nil
	for _, e := range events {
		line, err := Encode(e)
		if err != nil {
			s.report(err)
			continue
		}
		s.watchers = slices.DeleteFunc(s.watchers, func(w *watcher) bool {
			select {
			case w.lines <- line:
				return false
			default:
				close(w.lines)
				return true
			}
		})
	}
	return nil
}
