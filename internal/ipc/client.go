package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"
)

var ErrNoSocket = errors.New("ipc: no lys-wm socket")

func FindSocket(getenv func(string) string) (string, error) {
	if path := getenv(wmipc.SocketEnv); path != "" {
		return path, nil
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: neither %s nor XDG_RUNTIME_DIR is set", ErrNoSocket, wmipc.SocketEnv)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "lys-wm", "*.sock"))
	if err != nil {
		return "", fmt.Errorf("ipc: look for sockets: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("%w: %s is unset and %s holds %d sockets %v", ErrNoSocket, wmipc.SocketEnv, filepath.Join(dir, "lys-wm"), len(matches), matches)
	}
	return matches[0], nil
}

const (
	upgradeTimeout = 2 * time.Minute
	pickTimeout    = 2 * time.Minute
)

type conn struct {
	wait time.Duration
	c    *net.UnixConn
	r    *bufio.Reader
}

func send(path string, req Request) (*conn, error) {
	c, err := net.DialTimeout("unix", path, ioTimeout)
	if err != nil {
		return nil, fmt.Errorf("ipc: connect to %s: %w", path, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("ipc: %s is not a Unix socket", path)
	}
	line, err := json.Marshal(req)
	if err == nil {
		err = write(uc, append(line, '\n'))
	}
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ipc: send %s: %w", req.Request, err)
	}
	return &conn{c: uc, r: bufio.NewReader(uc)}, nil
}

func (cn *conn) response() (json.RawMessage, error) {
	wait := ioTimeout
	if cn.wait > 0 {
		wait = cn.wait
	}
	if err := cn.c.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return nil, err
	}
	line, err := cn.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("ipc: read the response: %w", err)
	}
	var resp wmipc.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("ipc: decode the response %q: %w", line, err)
	}
	if resp.Version != wmipc.Version {
		return nil, fmt.Errorf("ipc: lys-wm speaks IPC version %d, this client version %d", resp.Version, wmipc.Version)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("lys-wm: %s", resp.Error)
	}
	return resp.Result, nil
}

func (cn *conn) close() error {
	if err := cn.c.Close(); err != nil {
		return fmt.Errorf("ipc: close: %w", err)
	}
	return nil
}

func Do(path string, req Request) (result json.RawMessage, err error) {
	if req.Request == wmipc.KindWatch {
		return nil, fmt.Errorf("%w: use Watch for the event stream", ErrBadRequest)
	}
	if req.Request == wmipc.KindCapture {
		return nil, fmt.Errorf("%w: use OpenCapture for an input capture", ErrBadRequest)
	}
	cn, err := send(path, req)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, cn.close()) }()
	switch req.Request {
	case wmipc.KindUpgrade:
		cn.wait = upgradeTimeout
	case wmipc.KindPickWindow:
		cn.wait = pickTimeout
	}
	return cn.response()
}

type Stream struct {
	cn *conn
}

func Subscribe(path string, snapshot bool) (*Stream, error) {
	cn, err := send(path, Request{Request: wmipc.KindWatch, Snapshot: snapshot})
	if err != nil {
		return nil, err
	}
	if _, err := cn.response(); err != nil {
		return nil, errors.Join(err, cn.close())
	}
	if err := cn.c.SetReadDeadline(time.Time{}); err != nil {
		return nil, errors.Join(err, cn.close())
	}
	return &Stream{cn: cn}, nil
}

func (s *Stream) Next() ([]byte, error) {
	line, err := s.cn.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("ipc: read the event stream: %w", err)
	}
	return line, nil
}

func (s *Stream) Close() error {
	return s.cn.close()
}

type CaptureConn struct {
	cn *conn
}

func OpenCapture(path string) (*CaptureConn, error) {
	cn, err := send(path, Request{Request: wmipc.KindCapture})
	if err != nil {
		return nil, err
	}
	if _, err := cn.response(); err != nil {
		return nil, errors.Join(err, cn.close())
	}
	if err := cn.c.SetReadDeadline(time.Time{}); err != nil {
		return nil, errors.Join(err, cn.close())
	}
	return &CaptureConn{cn: cn}, nil
}

func (c *CaptureConn) Send(cmd CaptureCommand) error {
	line, err := EncodeCaptureCommand(cmd)
	if err != nil {
		return err
	}
	if err := write(c.cn.c, line); err != nil {
		return fmt.Errorf("ipc: send a capture command: %w", err)
	}
	return nil
}

func (c *CaptureConn) Next() ([]byte, error) {
	line, err := c.cn.r.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("ipc: read the capture stream: %w", err)
	}
	return line, nil
}

func (c *CaptureConn) Close() error {
	return c.cn.close()
}
