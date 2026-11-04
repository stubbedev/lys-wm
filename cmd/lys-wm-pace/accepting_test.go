package main

import (
	"net"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stubbedev/lys-wm/internal/testenv"
)

func TestASocketBoundButNotListeningIsNotAccepting(t *testing.T) {
	dir := testenv.SocketDir(t)
	bound := filepath.Join(dir, "bound.sock")
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: bound}); err != nil {
		t.Fatal(err)
	}
	if accepting(bound) {
		t.Fatal("a socket that has only been bound counted as accepting")
	}
	if err := syscall.Listen(fd, 1); err != nil {
		t.Fatal(err)
	}
	if !accepting(bound) {
		t.Fatal("a listening socket did not count as accepting")
	}
	if accepting(filepath.Join(dir, "missing.sock")) {
		t.Fatal("a missing socket counted as accepting")
	}
}

func TestAListenerCountsAsAccepting(t *testing.T) {
	path := filepath.Join(testenv.SocketDir(t), "listener.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if !accepting(path) {
		t.Fatal("a listener did not count as accepting")
	}
}
