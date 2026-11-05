package main

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

const paceConfig = `xwayland: {enabled: false}
decoration:
  rounding: 12
  shadow: {enabled: true, range: 24, render_power: 3, offset: [0, 8], color: "#00000060"}
  blur:
    enabled: true
    size: 8
    passes: 2
    tint: "#ffffff30"
    saturation: 1.4
    brightness: 1.05
    grain: 0.04
    refraction: {bezel: 16, amount: 8}
    specular: {strength: 0.6, width: 1}
gaps: {inner: 8, outer: 12}
animations:
  enabled: true
  curves:
    glide: {bezier: [0.32, 0.72, 0, 1]}
    settle: {spring: {stiffness: 170, damping: 26}}
  events:
    global: {curve: glide, duration: 250}
    windows_move: {curve: settle}
    windows_minimize: {curve: glide, duration: 450}
rules:
  - {title: ^glass$, float: true, size: 1000x600, opacity: 0.6}
  - {title: ^pile$, float: true, size: 1000x700}
`

const pileTitle = "pile"

const (
	eased        = 400 * time.Millisecond
	sprung       = 900 * time.Millisecond
	genieTime    = 600 * time.Millisecond
	midFlight    = 120 * time.Millisecond
	steady       = 3 * time.Second
	settleGap    = 150 * time.Millisecond
	pumpChunk    = 4 * time.Second
	bandHeight   = 64
	swipeEvents  = 30
	swipeStep    = 12.0
	swipeEvery   = 8 * time.Millisecond
	quiet        = 5 * time.Millisecond
	settleTries  = 20
	warmupRounds = 2
	gateRounds   = 5
)

type step struct {
	name       string
	act        func(*session)
	after      func(*session)
	measure    time.Duration
	continuous bool
}

type scenario struct {
	name      string
	setup     func(*session)
	steps     []step
	once      bool
	fakeInput bool
}

type mark struct {
	step       string
	continuous bool
	from, to   time.Duration
}

type session struct {
	client  *testclient.Client
	ipc     string
	now     func() time.Duration
	swipes  io.Writer
	rss     func() int64
	growth  []rssGrowth
	marks   []mark
	windows []*testclient.Window
	opened  *testclient.Window
}

func (s *session) dispatch(action string) {
	if _, err := ipc.Do(s.ipc, ipc.Request{Request: ipc.KindDispatch, Action: action}); err != nil {
		panic(fatal{fmt.Errorf("dispatch %q: %w", action, err)})
	}
}

func (s *session) pump(d time.Duration) {
	for d > 0 {
		chunk := min(d, pumpChunk)
		until := time.Now().Add(chunk)
		s.client.DispatchUntil("the pump's clock", func() bool { return !time.Now().Before(until) })
		d -= chunk
	}
}

func (s *session) window(title string, paint testclient.Painter) *testclient.Window {
	w := s.client.Window(title, paint)
	s.client.DispatchUntil("window "+title+" to map", w.Mapped)
	s.client.DispatchUntil("window "+title+" to reach the layout", func() bool { return s.placed(title) })
	s.windows = append(s.windows, w)
	return w
}

func (s *session) placed(title string) bool {
	data, err := ipc.Do(s.ipc, ipc.Request{Request: ipc.KindWindows})
	if err != nil {
		panic(fatal{fmt.Errorf("list the windows: %w", err)})
	}
	var windows []ipc.Window
	if err := json.Unmarshal(data, &windows); err != nil {
		panic(fatal{fmt.Errorf("read the windows %s: %w", data, err)})
	}
	return slices.ContainsFunc(windows, func(w ipc.Window) bool { return w.Title == title })
}

func (s *session) still(title string, b, g, r byte) {
	s.window(title, testclient.Fill(b, g, r)).Stop()
}

func movingBand(pix []byte, stride int, size image.Point, frame int) {
	for i := range pix {
		pix[i] = 0x50
	}
	top := frame * 8 % max(size.Y-bandHeight, 1)
	for y := top; y < min(top+bandHeight, size.Y); y++ {
		row := pix[y*stride : y*stride+size.X*4]
		for x := 0; x < len(row); x += 4 {
			row[x], row[x+1], row[x+2], row[x+3] = 0x20, 0xc0, 0xff, 0xff
		}
	}
}

func (s *session) settle() {
	s.pump(sprung)
	for range settleTries {
		from := s.now()
		s.client.Sync()
		took := s.now() - from
		if took < quiet {
			return
		}
		s.pump(2 * took)
	}
}

func (s *session) clear() {
	for _, w := range s.windows {
		if err := w.Destroy(); err != nil {
			panic(fatal{fmt.Errorf("destroy a window: %w", err)})
		}
	}
	s.windows = nil
	s.client.Sync()
	s.dispatch("workspace 1")
	s.settle()
}

func (s *session) play(st *step) {
	from := s.now()
	st.act(s)
	s.pump(st.measure)
	s.marks = append(s.marks, mark{step: st.name, continuous: st.continuous, from: from, to: s.now()})
	s.pump(settleGap)
	if st.after != nil {
		s.settle()
		st.after(s)
	}
}

func (s *session) run(sc *scenario, rounds int) {
	if sc.setup != nil {
		sc.setup(s)
		s.settle()
	}
	if sc.once {
		rounds = 1
	}
	samples := make([]int64, 0, rounds)
	for round := range rounds {
		for i := range sc.steps {
			s.play(&sc.steps[i])
		}
		if round >= warmupRounds-1 {
			samples = append(samples, s.rss())
		}
	}
	if rounds > warmupRounds {
		s.growth = append(s.growth, rssGrowth{Scenario: sc.name, KiB: repeatedGrowth(samples)})
	}
	s.clear()
}

func (s *session) swipe(line string) {
	if _, err := io.WriteString(s.swipes, line+"\n"); err != nil {
		panic(fatal{fmt.Errorf("send the fake touchpad %q: %w", line, err)})
	}
}

func fingers(dx, dy float64) func(*session) {
	return func(s *session) {
		s.swipe("begin")
		line := "swipe " + strconv.FormatFloat(dx, 'f', 1, 64) + " " + strconv.FormatFloat(dy, 'f', 1, 64)
		onTicks(swipeEvents, swipeEvery, s.now, time.Sleep, func() { s.swipe(line) })
		s.client.Sync()
	}
}

func onTicks(n int, every time.Duration, now func() time.Duration, sleep func(time.Duration), tick func()) {
	start := now()
	for i := range n {
		sleep(start + time.Duration(i)*every - now())
		tick()
	}
}

func lifted(s *session) {
	s.swipe("end")
}

func showsWindow(title string) func(*session) {
	return func(s *session) {
		data, err := ipc.Do(s.ipc, ipc.Request{Request: ipc.KindActiveWindow})
		if err != nil {
			panic(fatal{fmt.Errorf("ask for the active window: %w", err)})
		}
		var w ipc.Window
		if err := json.Unmarshal(data, &w); err != nil {
			panic(fatal{fmt.Errorf("read the active window %s: %w", data, err)})
		}
		if w.Title != title {
			panic(fatal{fmt.Errorf("the swipe left window %q active, want %q", w.Title, title)})
		}
	}
}

func overviewIs(open bool) func(*session) {
	return func(s *session) {
		data, err := ipc.Do(s.ipc, ipc.Request{Request: ipc.KindOverview})
		if err != nil {
			panic(fatal{fmt.Errorf("ask whether the overview is open: %w", err)})
		}
		var got bool
		if err := json.Unmarshal(data, &got); err != nil {
			panic(fatal{fmt.Errorf("read whether the overview is open from %s: %w", data, err)})
		}
		if got != open {
			panic(fatal{fmt.Errorf("the overview is open: %v, want %v", got, open)})
		}
	}
}

func pile(n int) func(*session) {
	return func(s *session) {
		for i := range n {
			shade := byte(0x20 + i*5)
			s.still(pileTitle, shade, 0x60, 0xa0-shade/2)
		}
	}
}

func overviewByAction(n int) scenario {
	name := "overview-" + strconv.Itoa(n)
	return scenario{
		name:  name,
		setup: pile(n),
		steps: []step{
			{name: name + "-open", measure: sprung, act: dispatches("overview show"), after: overviewIs(true)},
			{name: name + "-close", measure: sprung, act: dispatches("overview hide"), after: overviewIs(false)},
		},
	}
}

func overviewByFingers(n int) scenario {
	name := "overview-scrub-" + strconv.Itoa(n)
	return scenario{
		name:      name,
		fakeInput: true,
		setup:     pile(n),
		steps: []step{
			{name: name + "-follow-open", continuous: true, act: fingers(0, -swipeStep)},
			{name: name + "-release-open", measure: sprung, act: lifted, after: overviewIs(true)},
			{name: name + "-follow-close", continuous: true, act: fingers(0, swipeStep)},
			{name: name + "-release-close", measure: sprung, act: lifted, after: overviewIs(false)},
		},
	}
}

func dispatches(action string) func(*session) {
	return func(s *session) { s.dispatch(action) }
}

func twoWindows(s *session) {
	s.still("left", 0x40, 0x30, 0x20)
	s.still("right", 0x20, 0x30, 0x40)
}

func twoWorkspaces(s *session) {
	s.still("first", 0x40, 0x30, 0x20)
	s.still("first too", 0x20, 0x40, 0x30)
	s.dispatch("workspace 2")
	s.pump(eased)
	s.still("second", 0x20, 0x30, 0x40)
	s.dispatch("workspace 1")
}

var scenarios = []scenario{
	{
		name: "window",
		setup: func(s *session) {
			s.still("base", 0x40, 0x30, 0x20)
		},
		steps: []step{
			{name: "window-open", measure: eased, act: func(s *session) {
				s.opened = s.client.Window("opened", testclient.Fill(0x20, 0x30, 0x40))
				s.client.DispatchUntil("the opened window to map", s.opened.Mapped)
				s.opened.Stop()
			}},
			{name: "window-close", measure: eased, act: func(s *session) {
				if err := s.opened.Destroy(); err != nil {
					panic(fatal{fmt.Errorf("close the window: %w", err)})
				}
				s.client.Sync()
			}},
		},
	},
	{
		name:  "move",
		setup: twoWindows,
		steps: []step{
			{name: "move-spring-left", measure: sprung, act: dispatches("move left")},
			{name: "move-spring-right", measure: sprung, act: dispatches("move right")},
			{name: "move-interrupted", measure: sprung, act: func(s *session) {
				s.dispatch("move left")
				s.pump(midFlight)
				s.dispatch("move right")
			}},
		},
	},
	{
		name:  "resize",
		setup: twoWindows,
		steps: []step{
			{name: "resize-grow", measure: sprung, act: dispatches("resizeactive 120 0")},
			{name: "resize-shrink", measure: sprung, act: dispatches("resizeactive -120 0")},
		},
	},
	{
		name:  "workspace",
		setup: twoWorkspaces,
		steps: []step{
			{name: "workspace-there", measure: eased, act: dispatches("workspace 2")},
			{name: "workspace-back", measure: eased, act: dispatches("workspace 1")},
		},
	},
	{
		name:      "swipe",
		fakeInput: true,
		setup:     twoWorkspaces,
		steps: []step{
			{name: "swipe-follow-there", measure: 0, continuous: true, act: fingers(-swipeStep, 0)},
			{name: "swipe-release-there", measure: sprung, act: lifted, after: showsWindow("second")},
			{name: "swipe-follow-back", measure: 0, continuous: true, act: fingers(swipeStep, 0)},
			{name: "swipe-release-back", measure: sprung, act: lifted, after: showsWindow("first too")},
		},
	},
	{
		name: "genie",
		setup: func(s *session) {
			s.still("base", 0x40, 0x30, 0x20)
			s.still("minimized", 0x20, 0x30, 0x40)
		},
		steps: []step{
			{name: "genie-minimize", measure: genieTime, act: dispatches("minimize")},
			{name: "genie-restore", measure: genieTime, act: dispatches("restore")},
		},
	},
	{
		name: "glass-busy",
		once: true,
		setup: func(s *session) {
			s.window("busy", movingBand)
			s.still("glass", 0xf0, 0xf0, 0xf0)
		},
		steps: []step{{name: "glass-over-busy", measure: steady, continuous: true, act: func(*session) {}}},
	},
	{
		name: "glass-still",
		once: true,
		setup: func(s *session) {
			s.still("base", 0x40, 0x30, 0x20)
			s.still("glass", 0xf0, 0xf0, 0xf0)
		},
		steps: []step{{name: "glass-over-still", measure: steady, act: func(*session) {}}},
	},
	{
		name: "glass-slide",
		setup: func(s *session) {
			s.still("base", 0x40, 0x30, 0x20)
			s.still("glass", 0xf0, 0xf0, 0xf0)
			s.dispatch("workspace 2")
			s.pump(eased)
			s.still("second", 0x20, 0x30, 0x40)
			s.dispatch("workspace 1")
		},
		steps: []step{
			{name: "glass-slide-there", measure: eased, act: dispatches("workspace 2")},
			{name: "glass-slide-back", measure: eased, act: dispatches("workspace 1")},
		},
	},
	overviewByAction(10),
	overviewByFingers(10),
	overviewByAction(30),
	overviewByFingers(30),
}

func scenarioNames() []string {
	names := make([]string, len(scenarios))
	for i := range scenarios {
		names[i] = scenarios[i].name
	}
	return names
}

func millis(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 2, 64)
}
