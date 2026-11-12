// Package loop is the single-threaded event loop that owns compositor state.
package loop

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Events uint32

const (
	Readable Events = unix.EPOLLIN
	Writable Events = unix.EPOLLOUT
	Hangup   Events = unix.EPOLLHUP | unix.EPOLLRDHUP
	Error    Events = unix.EPOLLERR
	Edge     Events = unix.EPOLLET
	OneShot  Events = unix.EPOLLONESHOT
)

var ErrClosed = errors.New("loop: closed")

var errWatched = errors.New("already watched by a source that has not been removed")

type token uint64

func (t token) event(events Events) unix.EpollEvent {
	return unix.EpollEvent{Events: uint32(events), Fd: int32(uint32(t)), Pad: int32(uint32(t >> 32))} //nolint:gosec // packs the token into epoll's 64-bit data union
}

func tokenOf(ev *unix.EpollEvent) token {
	return token(uint32(ev.Fd)) | token(uint32(ev.Pad))<<32 //nolint:gosec // unpacks the token from epoll's 64-bit data union
}

type source struct {
	fd      int
	owned   bool
	handler func(Events) error
}

type Loop struct {
	epfd     int
	sources  map[token]*source
	watched  map[int]token
	next     token
	events   []unix.EpollEvent
	pending  []unix.EpollEvent
	idle     []func() error
	running  []func() error
	before   []func() error
	flush    []func() error
	stopped  bool
	closed   bool
	wakeups  int
	epoch    time.Time
	manual   *Manual
	timers   []*Timer
	due      []*Timer
	seq      uint64
	noPwait2 bool
	busy     time.Duration

	mailboxFD int
	mu        sync.Mutex
	posted    []func() error
	received  []func() error
	shut      bool

	signals []*Signals
}

func New() (*Loop, error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("loop: epoll_create1: %w", err)
	}
	l := &Loop{epfd: epfd, sources: map[token]*source{}, watched: map[int]token{}, events: make([]unix.EpollEvent, 64), epoch: time.Now()}
	efd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		_ = unix.Close(epfd)
		return nil, fmt.Errorf("loop: eventfd: %w", err)
	}
	l.mailboxFD = efd
	if _, err := l.add(efd, true, Readable, l.drainMailbox); err != nil {
		_ = unix.Close(efd)
		_ = unix.Close(epfd)
		return nil, err
	}
	return l, nil
}

type Manual struct {
	loop *Loop
	now  time.Duration
	dry  func()
}

func NewManual(start time.Duration) (*Loop, *Manual, error) {
	l, err := New()
	if err != nil {
		return nil, nil, err
	}
	l.manual = &Manual{loop: l, now: start}
	return l, l.manual, nil
}

func (m *Manual) Now() time.Duration {
	return m.now
}

func (m *Manual) Set(now time.Duration) {
	m.now = now
}

func (m *Manual) NextTimer() (time.Duration, bool) {
	return m.loop.nextDeadline()
}

func (m *Manual) WhenDry(f func()) {
	m.dry = f
}

func (l *Loop) elapsed() time.Duration {
	if l.manual != nil {
		return l.manual.now
	}
	return time.Since(l.epoch)
}

func (l *Loop) deadline(d time.Duration) time.Duration {
	if l.manual != nil {
		return l.manual.now + max(d, 0)
	}
	return time.Since(l.epoch) + max(d, 1)
}

func (l *Loop) add(fd int, owned bool, events Events, handler func(Events) error) (token, error) {
	if l.closed {
		return 0, ErrClosed
	}
	if _, ok := l.watched[fd]; ok {
		return 0, fmt.Errorf("loop: add fd %d: %w", fd, errWatched)
	}
	l.next++
	t := l.next
	ev := t.event(events)
	if err := unix.EpollCtl(l.epfd, unix.EPOLL_CTL_ADD, fd, &ev); err != nil {
		return 0, fmt.Errorf("loop: add fd %d: %w", fd, err)
	}
	l.sources[t] = &source{fd: fd, owned: owned, handler: handler}
	l.watched[fd] = t
	return t, nil
}

func (l *Loop) modify(t token, events Events) error {
	s, ok := l.sources[t]
	if !ok {
		return ErrClosed
	}
	ev := t.event(events)
	if err := unix.EpollCtl(l.epfd, unix.EPOLL_CTL_MOD, s.fd, &ev); err != nil {
		return fmt.Errorf("loop: modify fd %d: %w", s.fd, err)
	}
	return nil
}

func (l *Loop) remove(t token) error {
	s, ok := l.sources[t]
	if !ok {
		return nil
	}
	delete(l.sources, t)
	delete(l.watched, s.fd)
	var errs []error
	if err := unix.EpollCtl(l.epfd, unix.EPOLL_CTL_DEL, s.fd, nil); err != nil && !errors.Is(err, unix.EBADF) && !errors.Is(err, unix.ENOENT) {
		errs = append(errs, fmt.Errorf("loop: remove fd %d: %w", s.fd, err))
	}
	if s.owned {
		if err := unix.Close(s.fd); err != nil {
			errs = append(errs, fmt.Errorf("loop: close fd %d: %w", s.fd, err))
		}
	}
	return errors.Join(errs...)
}

type Source struct {
	loop  *Loop
	token token
}

func (l *Loop) AddFD(fd int, events Events, handler func(Events) error) (*Source, error) {
	t, err := l.add(fd, false, events, handler)
	if err != nil {
		return nil, err
	}
	return &Source{loop: l, token: t}, nil
}

func (l *Loop) Once(fd int, handler func() error) error {
	var t token
	var err error
	t, err = l.add(fd, true, Readable, func(Events) error {
		return errors.Join(l.remove(t), handler())
	})
	if err != nil {
		return errors.Join(err, unix.Close(fd))
	}
	return nil
}

func (s *Source) Modify(events Events) error {
	return s.loop.modify(s.token, events)
}

func (s *Source) Remove() error {
	return s.loop.remove(s.token)
}

type Timer struct {
	loop    *Loop
	handler func() error
	at      time.Duration
	seq     uint64
	armed   bool
	removed bool
}

func (l *Loop) AddTimer(handler func() error) (*Timer, error) {
	if l.closed {
		return nil, ErrClosed
	}
	t := &Timer{loop: l, handler: handler}
	l.timers = append(l.timers, t)
	return t, nil
}

func (t *Timer) Arm(d time.Duration) error {
	if t.removed {
		return ErrClosed
	}
	l := t.loop
	l.seq++
	t.at, t.seq, t.armed = l.deadline(d), l.seq, true
	return nil
}

func (t *Timer) Disarm() error {
	if t.removed {
		return ErrClosed
	}
	t.armed = false
	return nil
}

func (t *Timer) Remove() error {
	if t.removed {
		return nil
	}
	t.removed, t.armed = true, false
	l := t.loop
	if i := slices.Index(l.timers, t); i >= 0 {
		l.timers = slices.Delete(l.timers, i, i+1)
	}
	return nil
}

func (l *Loop) nextDeadline() (time.Duration, bool) {
	var next time.Duration
	found := false
	for _, t := range l.timers {
		if t.armed && (!found || t.at < next) {
			next, found = t.at, true
		}
	}
	return next, found
}

func (l *Loop) fireTimers() (bool, error) {
	now := l.elapsed()
	l.due = l.due[:0]
	for _, t := range l.timers {
		if t.armed && t.at <= now {
			l.due = append(l.due, t)
		}
	}
	if len(l.due) == 0 {
		return false, nil
	}
	slices.SortFunc(l.due, func(a, b *Timer) int { return cmp.Compare(a.seq, b.seq) })
	for i, t := range l.due {
		l.due[i] = nil
		if !t.armed || t.at > now || l.stopped {
			continue
		}
		t.armed = false
		if err := t.handler(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (l *Loop) wait(block time.Duration) (int, error) {
	if block <= 0 || l.noPwait2 {
		ms := -1
		if block >= 0 {
			ms = int((block + time.Millisecond - 1) / time.Millisecond)
		}
		return unix.EpollWait(l.epfd, l.events, ms)
	}
	ts := unix.NsecToTimespec(block.Nanoseconds())
	n, _, errno := unix.Syscall6(unix.SYS_EPOLL_PWAIT2, uintptr(l.epfd), uintptr(unsafe.Pointer(&l.events[0])), uintptr(len(l.events)), uintptr(unsafe.Pointer(&ts)), 0, 0) //nolint:gosec // the kernel reads and fills these during the call
	if errno == unix.ENOSYS {
		l.noPwait2 = true
		return l.wait(block)
	}
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

type Signals struct {
	loop *Loop
	ch   chan os.Signal
	stop chan struct{}
	done chan struct{}
}

func (l *Loop) AddSignals(handler func(os.Signal) error, sigs ...os.Signal) (*Signals, error) {
	if l.closed {
		return nil, ErrClosed
	}
	s := &Signals{loop: l, ch: make(chan os.Signal, len(sigs)), stop: make(chan struct{}), done: make(chan struct{})}
	signal.Notify(s.ch, sigs...)
	go func() {
		defer close(s.done)
		for {
			select {
			case sig := <-s.ch:
				l.Hand("signal "+sig.String(), func() error { return handler(sig) })
			case <-s.stop:
				return
			}
		}
	}()
	l.signals = append(l.signals, s)
	return s, nil
}

func (s *Signals) Remove() {
	if i := slices.Index(s.loop.signals, s); i >= 0 {
		s.loop.signals = slices.Delete(s.loop.signals, i, i+1)
		signal.Stop(s.ch)
		close(s.stop)
		<-s.done
	}
}

func (l *Loop) Post(f func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.shut {
		return ErrClosed
	}
	wake := len(l.posted) == 0
	l.posted = append(l.posted, f)
	if !wake {
		return nil
	}
	one := [8]byte{1}
	if _, err := unix.Write(l.mailboxFD, one[:]); err != nil && !errors.Is(err, unix.EAGAIN) {
		l.posted[0] = nil
		l.posted = l.posted[:0]
		return fmt.Errorf("loop: wake mailbox fd %d: %w", l.mailboxFD, err)
	}
	return nil
}

func (l *Loop) Hand(what string, f func() error) {
	if err := l.Post(f); err != nil && !errors.Is(err, ErrClosed) {
		panic(fmt.Errorf("loop: hand %s back to the loop: %w", what, err))
	}
}

func (l *Loop) drainMailbox(Events) error {
	var buf [8]byte
	if _, err := unix.Read(l.mailboxFD, buf[:]); err != nil && !errors.Is(err, unix.EAGAIN) {
		return fmt.Errorf("loop: read mailbox: %w", err)
	}
	l.mu.Lock()
	l.received, l.posted = l.posted, l.received[:0]
	l.mu.Unlock()
	for i, f := range l.received {
		l.received[i] = nil
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) Idle(f func() error) {
	l.idle = append(l.idle, f)
}

func (l *Loop) runIdle() error {
	if l.stopped {
		return nil
	}
	l.running, l.idle = l.idle, l.running[:0]
	for i, f := range l.running {
		l.running[i] = nil
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) BeforeWait(f func() error) {
	l.before = append(l.before, f)
}

func (l *Loop) BeforeIdle(f func() error) {
	l.flush = append(l.flush, f)
}

func (l *Loop) flushBeforeIdle() error {
	if len(l.idle) == 0 || l.stopped {
		return nil
	}
	for _, f := range l.flush {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) Wakeups() int {
	return l.wakeups
}

func (l *Loop) Busy() time.Duration {
	return l.busy
}

func (l *Loop) Stop() {
	l.stopped = true
}

func (l *Loop) Run() error {
	if l.closed {
		return ErrClosed
	}
	l.stopped = false
	woke := l.elapsed()
	defer func() { l.busy += l.elapsed() - woke }()
	for !l.stopped {
		if err := l.flushBeforeIdle(); err != nil {
			return err
		}
		if err := l.runIdle(); err != nil {
			return err
		}
		for _, f := range l.before {
			if err := f(); err != nil {
				return err
			}
		}
		if l.stopped {
			break
		}
		l.busy += l.elapsed() - woke
		n, err := l.poll()
		woke = l.elapsed()
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("loop: epoll_wait: %w", err)
		}
		l.pending = l.events[:n]
		if err := l.dispatch(); err != nil {
			return err
		}
		fired, err := l.fireTimers()
		if n > 0 || fired {
			l.wakeups++
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) poll() (int, error) {
	if l.manual != nil {
		return l.pollManual()
	}
	block := time.Duration(-1)
	if len(l.idle) > 0 {
		block = 0
	} else if at, ok := l.nextDeadline(); ok {
		block = max(at-time.Since(l.epoch), 0)
	}
	return l.wait(block)
}

func (l *Loop) runnable() bool {
	if len(l.idle) > 0 {
		return true
	}
	at, ok := l.nextDeadline()
	return ok && at <= l.manual.now
}

func (l *Loop) pollManual() (int, error) {
	n, err := l.wait(0)
	if n != 0 || err != nil || l.runnable() {
		return n, err
	}
	if l.manual.dry != nil {
		l.manual.dry()
	}
	if l.stopped || l.runnable() {
		return 0, nil
	}
	return l.wait(-1)
}

func (l *Loop) dispatch() error {
	for len(l.pending) > 0 && !l.stopped {
		ev := &l.pending[0]
		l.pending = l.pending[1:]
		s, ok := l.sources[tokenOf(ev)]
		if !ok {
			continue
		}
		if err := s.handler(Events(ev.Events)); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) Close() error {
	if l.closed {
		return nil
	}
	l.mu.Lock()
	l.shut = true
	l.mu.Unlock()
	for len(l.signals) > 0 {
		l.signals[0].Remove()
	}
	var errs []error
	for _, t := range slices.Clone(l.timers) {
		errs = append(errs, t.Remove())
	}
	for _, t := range slices.Sorted(maps.Keys(l.sources)) {
		errs = append(errs, l.remove(t))
	}
	l.closed = true
	if err := unix.Close(l.epfd); err != nil {
		errs = append(errs, fmt.Errorf("loop: close epoll fd %d: %w", l.epfd, err))
	}
	return errors.Join(errs...)
}
