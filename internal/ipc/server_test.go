package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

type fake struct {
	snap       Snapshot
	dispatched []wm.Action
	icons      map[wm.WindowID]*image.RGBA
	server     *Server
	snapshots  int
	created    []OutputSpec
	removed    []string
	captures   []*fakeCapture
	pick       func(*wm.WindowID, error)
	cancelled  int
}

func (f *fake) CreateOutput(spec OutputSpec) (CreatedOutput, error) {
	f.created = append(f.created, spec)
	return CreatedOutput{Name: spec.Name}, nil
}

func (f *fake) RemoveOutput(name string) error {
	if name != "VIRT-1" {
		return fmt.Errorf("no virtual output %s", name)
	}
	f.removed = append(f.removed, name)
	return nil
}

func (f *fake) Icon(id wm.WindowID, _ int) (*image.RGBA, int, error) {
	img, ok := f.icons[id]
	if !ok {
		return nil, 0, fmt.Errorf("no icon for window %d", id)
	}
	return img, 1, nil
}

func (f *fake) Snapshot() Snapshot {
	f.snapshots++
	snap := f.snap
	snap.Windows = append([]Window(nil), f.snap.Windows...)
	return snap
}

func (f *fake) PickWindow(answer func(*wm.WindowID, error)) (func(), error) {
	if f.pick != nil {
		return nil, errors.New("a pick is running")
	}
	f.pick = answer
	return func() { f.cancelled++; f.pick = nil }, nil
}

func (f *fake) PrepareDispatch(wm.Action) func() error { return nil }

func (f *fake) DispatchPrepared(a wm.Action, _ func() error) error { return f.Dispatch(a) }

func (f *fake) Dispatch(a wm.Action) error {
	if _, ok := a.(wm.KillAction); ok {
		return errors.New("nothing to kill")
	}
	f.dispatched = append(f.dispatched, a)
	f.server.Dispatched(a)
	return nil
}

type rig struct {
	t      *testing.T
	loop   *loop.Loop
	server *Server
	fake   *fake
	path   string
	errs   chan error
}

func start(t *testing.T) *rig {
	t.Helper()
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, loop: l, fake: &fake{}, path: filepath.Join(testenv.SocketDir(t), "lys-wm", "wayland-9.sock"), errs: make(chan error, 16)}
	if r.server, err = Listen(l, r.path, r.fake, func(err error) { r.errs <- err }); err != nil {
		t.Fatal(err)
	}
	r.fake.server = r.server
	done := make(chan error, 1)
	go func() { done <- l.Run() }()
	t.Cleanup(func() {
		if err := l.Post(func() error {
			l.Stop()
			return nil
		}); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
		if err := r.server.Close(); err != nil {
			t.Error(err)
		}
		if err := l.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-r.errs:
			t.Errorf("reported: %v", err)
		default:
		}
	})
	return r
}

func (r *rig) do(f func(*fake)) {
	r.t.Helper()
	ran := make(chan struct{})
	if err := r.loop.Post(func() error {
		f(r.fake)
		r.server.Changed()
		close(ran)
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
	<-ran
}

func (r *rig) raw(line string) wmipc.Response {
	r.t.Helper()
	c, err := net.Dial("unix", r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(line)); err != nil {
		r.t.Fatal(err)
	}
	if err := c.(*net.UnixConn).CloseWrite(); err != nil {
		r.t.Fatal(err)
	}
	var resp wmipc.Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		r.t.Fatal(err)
	}
	return resp
}

func TestQueriesAndDispatch(t *testing.T) {
	r := start(t)
	if got, err := Do(r.path, Request{Request: wmipc.KindVersion}); err != nil || string(got) != "1" {
		t.Fatalf("version %s, %v", got, err)
	}
	if got, err := Do(r.path, Request{Request: wmipc.KindWindows}); err != nil || string(got) != "[]" {
		t.Fatalf("no windows gave %s, %v", got, err)
	}
	if got, err := Do(r.path, Request{Request: wmipc.KindActiveWindow}); err != nil || got != nil {
		t.Fatalf("no active window gave %s, %v", got, err)
	}
	r.do(func(f *fake) { f.snap.Windows = []Window{{ID: 7, Title: "seven", Focused: true}} })
	got, err := Do(r.path, Request{Request: wmipc.KindActiveWindow})
	var w Window
	if err != nil || json.Unmarshal(got, &w) != nil || w.ID != 7 || w.Title != "seven" {
		t.Fatalf("active window %s, %v", got, err)
	}
	if _, err := Do(r.path, Request{Request: wmipc.KindDispatch, Action: "workspace 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(r.path, Request{Request: wmipc.KindDispatch, Action: "kill"}); err == nil || err.Error() != "lys-wm: nothing to kill" {
		t.Fatalf("a failing dispatch: %v", err)
	}
	r.do(func(f *fake) {
		if len(f.dispatched) != 1 || f.dispatched[0] != (wm.WorkspaceAction{Target: wm.WorkspaceID(2)}) {
			t.Errorf("dispatched %v", f.dispatched)
		}
	})
	if _, err := Do(r.path, Request{Request: "teleport"}); err == nil || !strings.Contains(err.Error(), `unknown request "teleport"`) {
		t.Fatalf("an unknown request: %v", err)
	}
	if resp := r.raw(strings.Repeat("x", maxRequest+10)); !strings.Contains(resp.Error, "longer than") || resp.Version != wmipc.Version {
		t.Fatalf("an oversized request: %+v", resp)
	}
	if resp := r.raw(`{"request":"version"}`); resp.Error != "" || string(resp.Result) != "1" {
		t.Fatalf("a request without a newline: %+v", resp)
	}
}

func TestSubmapLayoutAndLocksAreQueries(t *testing.T) {
	r := start(t)
	for kind, want := range map[wmipc.Kind]string{wmipc.KindSubmap: `""`, wmipc.KindLayout: `{"index":0,"name":""}`, wmipc.KindLocks: `{"caps":false,"num":false,"scroll":false}`, wmipc.KindReducedMotion: `false`, wmipc.KindOverview: `false`} {
		if got, err := Do(r.path, Request{Request: kind}); err != nil || string(got) != want {
			t.Fatalf("%s before any change: %s, %v; want %s", kind, got, err, want)
		}
	}
	r.do(func(f *fake) {
		f.snap.Submap = "resize"
		f.snap.Layout = wmipc.Layout{Index: 1, Name: "Danish"}
		f.snap.Locks = wmipc.Locks{Num: true}
		f.snap.ReducedMotion = true
		f.snap.Overview = true
	})
	for kind, want := range map[wmipc.Kind]string{wmipc.KindSubmap: `"resize"`, wmipc.KindLayout: `{"index":1,"name":"Danish"}`, wmipc.KindLocks: `{"caps":false,"num":true,"scroll":false}`, wmipc.KindReducedMotion: `true`, wmipc.KindOverview: `true`} {
		if got, err := Do(r.path, Request{Request: kind}); err != nil || string(got) != want {
			t.Fatalf("%s: %s, %v; want %s", kind, got, err, want)
		}
	}
}

func watch(t *testing.T, path string) (<-chan string, func()) {
	t.Helper()
	lines := make(chan string, 1024)
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(`{"request":"watch"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	first, err := r.ReadString('\n')
	if err != nil || first != `{"version":1}`+"\n" {
		t.Fatalf("watch answered %q, %v", first, err)
	}
	go func() {
		defer close(lines)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(line)
		}
	}()
	return lines, func() { _ = c.Close() }
}

func next(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("the event stream ended")
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5s")
	}
	return ""
}

func TestIconsArePNGs(t *testing.T) {
	r := start(t)
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(1, 0, color.RGBA{R: 0x80, A: 0x80})
	r.do(func(f *fake) { f.icons = map[wm.WindowID]*image.RGBA{7: img} })
	got, err := Do(r.path, Request{Request: wmipc.KindIcon, Window: 7, Size: 64})
	var icon IconImage
	if err != nil || json.Unmarshal(got, &icon) != nil {
		t.Fatalf("icon %s, %v", got, err)
	}
	decoded, err := png.Decode(bytes.NewReader(icon.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if icon.Size != 2 || icon.Scale != 1 || decoded.Bounds() != img.Bounds() {
		t.Fatalf("icon of size %d at scale %d decoded to %v", icon.Size, icon.Scale, decoded.Bounds())
	}
	if r, _, _, a := decoded.At(1, 0).RGBA(); r != 0x8080 || a != 0x8080 {
		t.Fatalf("the half-transparent red pixel came back as %v", decoded.At(1, 0))
	}
	if _, err := Do(r.path, Request{Request: wmipc.KindIcon, Window: 8, Size: 64}); err == nil || err.Error() != "lys-wm: no icon for window 8" {
		t.Fatalf("a window without an icon: %v", err)
	}
}

func TestWatchStreamsDiffsAndDispatches(t *testing.T) {
	r := start(t)
	r.do(func(f *fake) { f.snap.Submap = "resize" })
	lines, stop := watch(t, r.path)
	defer stop()
	if _, err := Do(r.path, Request{Request: wmipc.KindDispatch, Action: "submap reset"}); err != nil {
		t.Fatal(err)
	}
	r.do(func(f *fake) {
		f.snap.Submap = ""
		f.snap.Windows = []Window{{ID: 1, Title: "one"}}
	})
	for _, want := range []string{
		`{"event":"dispatch","action":"submap reset"}`,
		`{"event":"open","window":{"id":1,"app_id":"","title":"one","pid":0,"workspace":0,"output":"","rect":{"x":0,"y":0,"width":0,"height":0},"floating":false,"fullscreen":false,"maximized":false,"client_fullscreen":false,"pinned":false,"minimized":false,"urgent":false,"unresponsive":false,"focused":false,"inhibits_shortcuts":false,"content_type":"none","content_source":"client","icon":null,"focus_timestamp":0,"group":null}}`,
		`{"event":"submap","submap":""}`,
	} {
		if got := next(t, lines); got != want {
			t.Fatalf("event %s, want %s", got, want)
		}
	}
}

func TestSlowWatchersAreDropped(t *testing.T) {
	r := start(t)
	c, err := net.Dial("unix", r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(`{"request":"watch"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	fast, stop := watch(t, r.path)
	defer stop()
	for i := range watchBacklog * 4 {
		r.do(func(f *fake) { f.snap.Submap = strings.Repeat("s", i%2+1) + strings.Repeat("x", 4096) })
		next(t, fast)
	}
	r.do(func(f *fake) {
		if n := len(r.server.watchers); n != 1 {
			t.Errorf("%d watchers left, want only the fast one", n)
		}
	})
}

func TestCloseWithClientsConnected(t *testing.T) {
	r := start(t)
	lines, stop := watch(t, r.path)
	defer stop()
	idle, err := net.Dial("unix", r.path)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	closed := make(chan error, 1)
	go func() { closed <- r.server.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ioTimeout / 2):
		t.Fatal("Close waited for connected clients")
	}
	if _, ok := <-lines; ok {
		t.Fatal("the event stream outlived the server")
	}
}

func TestFindSocket(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"XDG_RUNTIME_DIR": dir}
	getenv := func(k string) string { return env[k] }
	if _, err := FindSocket(getenv); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("no sockets: %v", err)
	}
	r := start(t)
	env["XDG_RUNTIME_DIR"] = filepath.Dir(filepath.Dir(r.path))
	if got, err := FindSocket(getenv); err != nil || got != r.path {
		t.Fatalf("one socket: %q, %v", got, err)
	}
	env[wmipc.SocketEnv] = "/elsewhere.sock"
	if got, err := FindSocket(getenv); err != nil || got != "/elsewhere.sock" {
		t.Fatalf("LYS_WM_SOCKET: %q, %v", got, err)
	}
}

type stopping struct {
	fake
	loop *loop.Loop
}

func (s *stopping) DispatchPrepared(a wm.Action, _ func() error) error { return s.Dispatch(a) }

func (s *stopping) Dispatch(wm.Action) error {
	s.loop.Stop()
	return nil
}

func TestADispatchThatStopsLysWMIsAnswered(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	for range 50 {
		l, err := loop.New()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(testenv.SocketDir(t), "lys-wm.sock")
		s, err := Listen(l, path, &stopping{loop: l}, func(err error) { t.Error(err) })
		if err != nil {
			t.Fatal(err)
		}
		answered := make(chan error, 1)
		go func() {
			_, err := Do(path, Request{Request: wmipc.KindDispatch, Action: "exit"})
			answered <- err
		}()
		if err := errors.Join(l.Run(), s.Close(), l.Close()); err != nil {
			t.Fatal(err)
		}
		if err := <-answered; err != nil {
			t.Fatalf("the exit that stopped lys-wm was answered with %v", err)
		}
	}
}

func TestAWakeupWithoutAChangeBuildsNoSnapshot(t *testing.T) {
	r := start(t)
	_, stop := watch(t, r.path)
	defer stop()
	count := func() int {
		n := make(chan int, 1)
		if err := r.loop.Post(func() error {
			n <- r.fake.snapshots
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return <-n
	}
	before := count()
	for range 10 {
		count()
	}
	if got := count() - before; got != 0 {
		t.Fatalf("%d snapshots across wakeups that changed nothing", got)
	}
	r.do(func(*fake) {})
	count()
	if got := count() - before; got != 1 {
		t.Fatalf("%d snapshots after one change, want 1", got)
	}
}

func TestOutputsAreCreatedAndRemovedOnTheLoop(t *testing.T) {
	r := start(t)
	got, err := Do(r.path, Request{Request: wmipc.KindCreateOutput, Name: "VIRT-1", Mode: &wmipc.Mode{Width: 1920, Height: 1080, RefreshMHz: 60000}, Scale: 1.5})
	var o CreatedOutput
	if err != nil || json.Unmarshal(got, &o) != nil || o.Name != "VIRT-1" {
		t.Fatalf("createoutput gave %s, %v", got, err)
	}
	if got, err := Do(r.path, Request{Request: wmipc.KindRemoveOutput, Name: "VIRT-1"}); err != nil || got != nil {
		t.Fatalf("removeoutput gave %s, %v", got, err)
	}
	if _, err := Do(r.path, Request{Request: wmipc.KindRemoveOutput, Name: "DP-1"}); err == nil || err.Error() != "lys-wm: no virtual output DP-1" {
		t.Fatalf("removing an output that is not virtual: %v", err)
	}
	r.do(func(f *fake) {
		want := OutputSpec{Name: "VIRT-1", Size: image.Pt(1920, 1080), RefreshMHz: 60000, Scale: 1.5}
		if len(f.created) != 1 || f.created[0] != want || len(f.removed) != 1 {
			t.Errorf("created %+v, removed %v", f.created, f.removed)
		}
	})
}

func TestABatchRunsInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	r := start(t)
	if _, err := Do(r.path, Request{Request: wmipc.KindDispatch, Actions: []string{"submap a", "submap b"}}); err != nil {
		t.Fatal(err)
	}
	_, err := Do(r.path, Request{Request: wmipc.KindDispatch, Actions: []string{"submap c", "kill", "submap d"}})
	if err == nil || !strings.Contains(err.Error(), "action 1 (kill): nothing to kill") {
		t.Fatalf("a batch failing at its second action: %v", err)
	}
	_, err = Do(r.path, Request{Request: wmipc.KindDispatch, Actions: []string{"submap e", "fly up"}})
	if err == nil || !strings.Contains(err.Error(), `action 1 (fly up)`) {
		t.Fatalf("a batch with an unknown action: %v", err)
	}
	r.do(func(f *fake) {
		var got []string
		for _, a := range f.dispatched {
			got = append(got, a.String())
		}
		if want := []string{"submap a", "submap b", "submap c"}; !slices.Equal(got, want) {
			t.Errorf("dispatched %v, want %v", got, want)
		}
	})
	if _, err := Do(r.path, Request{Request: wmipc.KindDispatch, Action: "submap a", Actions: []string{"submap b"}}); err == nil {
		t.Fatal("a dispatch with both an action and actions was accepted")
	}
}

func TestAWatchWithASnapshotStartsWithTheState(t *testing.T) {
	r := start(t)
	r.do(func(f *fake) {
		f.snap.Outputs = []Output{{Name: "DP-1", Workspace: 1, Focused: true}}
		f.snap.Windows = []Window{{ID: 1, Title: "one", Focused: true}}
		f.snap.Layers = []Layer{{ID: 4, Namespace: "bar", Layer: LayerTop, Output: "DP-1"}}
	})
	stream, err := Subscribe(r.path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	r.do(func(f *fake) { f.snap.Windows = append(f.snap.Windows, Window{ID: 2, Title: "two"}) })
	var got []string
	for len(got) < 7 {
		line, err := stream.Next()
		if err != nil {
			t.Fatal(err)
		}
		var e struct{ Event string }
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Event)
	}
	if want := []string{"output", "workspace", "focusedoutput", "open", "focus", "layer", "open"}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if _, err := Do(r.path, Request{Request: wmipc.KindWindows, Snapshot: true}); err == nil {
		t.Fatal("a query took a snapshot flag")
	}
}

func TestLayersAndDevicesAreQueries(t *testing.T) {
	r := start(t)
	r.do(func(f *fake) {
		f.snap.Layers = []Layer{{ID: 4, Namespace: "bar", Layer: LayerTop, Output: "DP-1", Rect: wmipc.Rect{Width: 100, Height: 30}, Keyboard: KeyboardOnDemand}}
		f.snap.Devices = []Device{{Name: "kbd", Capabilities: []Capability{CapKeyboard}, Layout: &wmipc.Layout{Name: "Danish"}}}
	})
	for kind, want := range map[wmipc.Kind]string{
		wmipc.KindLayers:  `[{"id":4,"namespace":"bar","layer":"top","output":"DP-1","rect":{"x":0,"y":0,"width":100,"height":30},"keyboard_interactivity":"on_demand"}]`,
		wmipc.KindDevices: `[{"name":"kbd","capabilities":["keyboard"],"layout":{"index":0,"name":"Danish"}}]`,
	} {
		if got, err := Do(r.path, Request{Request: kind}); err != nil || string(got) != want {
			t.Errorf("%s: %s, %v; want %s", kind, got, err, want)
		}
	}
}

func TestPickWindowAnswersWithThePickAndCancelsWhenTheClientLeaves(t *testing.T) {
	r := start(t)
	awaitPick := func() {
		t.Helper()
		deadline := time.Now().Add(testenv.HangLimit)
		for {
			armed := false
			r.do(func(f *fake) { armed = f.pick != nil })
			if armed {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the pick never started")
			}
			time.Sleep(time.Millisecond)
		}
	}
	answered := make(chan string, 1)
	go func() {
		got, err := Do(r.path, Request{Request: wmipc.KindPickWindow})
		answered <- fmt.Sprint(string(got), err)
	}()
	awaitPick()
	if _, err := Do(r.path, Request{Request: wmipc.KindPickWindow}); err == nil || !strings.Contains(err.Error(), "a pick is running") {
		t.Fatalf("a second pick: %v", err)
	}
	r.do(func(f *fake) {
		id := wm.WindowID(6)
		f.pick(&id, nil)
		f.pick = nil
	})
	if got := <-answered; got != "6<nil>" {
		t.Fatalf("pick answered %s", got)
	}
	c, err := net.Dial("unix", r.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(`{"request":"pickwindow"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	awaitPick()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(testenv.HangLimit)
	for cancelled := 0; cancelled != 1; {
		r.do(func(f *fake) { cancelled = f.cancelled })
		if time.Now().After(deadline) {
			t.Fatal("the pick of a client that left was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
}
