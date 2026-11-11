// Package compositor connects backend events to the server and draws each
// output's frames.
package compositor

import (
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stubbedev/lys-lib/drm"
	lgpu "github.com/stubbedev/lys-lib/gpu"
	"github.com/stubbedev/lys-lib/icontheme"
	gtext "github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/xcursor"

	"github.com/stubbedev/lys-wm/internal/alphamodifier"
	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/backgroundeffect"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/colormanager"
	"github.com/stubbedev/lys-wm/internal/colorrep"
	"github.com/stubbedev/lys-wm/internal/colorspace"
	"github.com/stubbedev/lys-wm/internal/committiming"
	"github.com/stubbedev/lys-wm/internal/contenttype"
	"github.com/stubbedev/lys-wm/internal/ctmcontrol"
	"github.com/stubbedev/lys-wm/internal/data"
	"github.com/stubbedev/lys-wm/internal/dmabuf"
	"github.com/stubbedev/lys-wm/internal/fifo"
	"github.com/stubbedev/lys-wm/internal/frametrace"
	"github.com/stubbedev/lys-wm/internal/gammacontrol"
	"github.com/stubbedev/lys-wm/internal/gpu"
	"github.com/stubbedev/lys-wm/internal/hyprlandsurface"
	"github.com/stubbedev/lys-wm/internal/idle"
	"github.com/stubbedev/lys-wm/internal/inputmethod"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/loop"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/outputmanagement"
	"github.com/stubbedev/lys-wm/internal/outputpower"
	shfg "github.com/stubbedev/lys-wm/internal/protocol/server/hyprlandfocusgrab"
	ssb "github.com/stubbedev/lys-wm/internal/protocol/server/systembell"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/shell"
	"github.com/stubbedev/lys-wm/internal/shm"
	"github.com/stubbedev/lys-wm/internal/singlepixel"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/syncobj"
	"github.com/stubbedev/lys-wm/internal/tabletinput"
	"github.com/stubbedev/lys-wm/internal/tearingcontrol"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xkb"
	"github.com/stubbedev/lys-wm/internal/xwayland"
)

var (
	Background = render.Color{R: 0x1e, G: 0x1e, B: 0x2e, A: 0xff}
	letterbox  = render.Color{A: 0xff}
)

type Config struct {
	Seat            seat.Config
	Cursor          CursorConfig
	WM              wm.Settings
	Binds           []binds.Bind
	Report          func(error)
	ExecOutput      ExecOutput
	Decor           Decor
	Font            *gtext.Typeface
	Icons           *icontheme.Theme
	Rules           []wm.Rule
	Swallow         Swallow
	Outputs         OutputRules
	VirtualOutputs  []VirtualOutput
	ColorManagement bool
	Tearing         bool
	Hooks           Hooks
	InheritTimeout  time.Duration
	HoldBound       time.Duration
	HiddenFrame     time.Duration
	PowerSaverRate  int
	HDRHold         time.Duration
	HoldOpening     bool
	Animations      anim.Table
	LayerRules      []wm.LayerRule
	WorkspaceRules  []WorkspaceRule
	Gestures        Gestures
	HotCorners      HotCorners
	Overview        OverviewStyle
	Switcher        SwitcherStyle
	Slower          map[render.Speed]Effects
	Clock           func() time.Duration
	ClientHotkeys   bool
	CursorBehavior  CursorBehavior
	DeviceOutputs   map[string]wm.OutputMatch
}

type ExecOutput struct {
	Stdout *os.File
	Stderr *os.File
}

type Hooks struct {
	Reload         func() (apply func() error)
	ParseProp      func(key, value string) (wm.Decision, error)
	Exit           func() error
	SwitchVT       func(vt uint32) error
	OpenFrameTrace func(path string) (*frametrace.Recorder, error)
	Lid            LidPolicy
}

type LidPolicy struct {
	Suspend   func() error
	OnBattery func() (bool, error)
}

type CursorConfig struct {
	Theme *xcursor.Theme
	Size  int
}

const DefaultCursorSize = 24

type Effects struct {
	Decor      Decor
	Animations anim.Table
	Overview   OverviewStyle
}

var SlowerSpeeds = [...]render.Speed{render.CPUDriver, render.Software}

func PlainSettings() wm.Settings {
	return wm.Settings{
		Collapse:  wm.CollapseEmptySplits,
		Autotile:  wm.Autotile{Enabled: true, Ephemeral: true, MinTrigger: 400, Divisor: 3},
		Peek:      wm.DefaultPeek,
		FloatStep: wm.DefaultFloatStep,
		Scroll:    wm.DefaultScrolling(),
		Layout:    wm.TreeLayout,
	}
}

func DefaultSettings() wm.Settings {
	s := PlainSettings()
	s.Gaps = wm.Gaps{Inner: lookUnit, Outer: wm.Insets{Top: lookUnit, Right: lookUnit, Bottom: lookUnit, Left: lookUnit}}
	return s
}

func PlainConfig(keymap *xkb.Keymap, font *gtext.Typeface) Config {
	return Config{
		Seat:           seat.Config{Keymap: keymap, RepeatRate: seat.DefaultRepeatRate, RepeatDelay: seat.DefaultRepeatDelay},
		Cursor:         CursorConfig{Size: DefaultCursorSize},
		WM:             PlainSettings(),
		Swallow:        Swallow{Proc: procFS},
		Report:         func(err error) { fmt.Fprintln(os.Stderr, "lys-wm:", err) },
		ExecOutput:     ExecOutput{Stdout: os.Stdout, Stderr: os.Stderr},
		Decor:          PlainDecor(),
		Font:           font,
		Icons:          icontheme.New("", nil),
		InheritTimeout: DefaultInheritTimeout,
		HoldBound:      DefaultHoldBound,
		HiddenFrame:    DefaultHiddenFrame,
		PowerSaverRate: DefaultPowerSaverRate,
		HDRHold:        DefaultHDRHold,
		Clock:          backend.Now,
		Gestures:       DefaultGestures(),
		HotCorners:     DefaultHotCorners(),
		Overview:       DefaultOverviewStyle(),
		Switcher:       DefaultSwitcherStyle(),
	}
}

func DefaultConfig(keymap *xkb.Keymap, font *gtext.Typeface) Config {
	cfg := PlainConfig(keymap, font)
	cfg.WM, cfg.Decor, cfg.Animations = DefaultSettings(), DefaultDecor(DefaultAccent), anim.MustResolve(anim.DefaultMotion())
	cfg.Slower = map[render.Speed]Effects{}
	for _, s := range SlowerSpeeds {
		l := DefaultLook(s, DefaultAccent)
		cfg.Slower[s] = Effects{Decor: l.Decor, Animations: anim.MustResolve(l.Motion), Overview: l.Overview}
	}
	return cfg
}

type Compositor struct {
	outputs            []*output.Output
	layout             *output.Manager
	renderer           render.Renderer
	frames             *frametrace.Recorder
	surfaces           *surface.Manager
	shm                *shm.Manager
	seat               *seat.Seat
	keyboards          []backend.KeyboardLEDs
	deviceOutputs      map[string]wm.OutputMatch
	devices            []backend.Device
	leds               xkb.Locks
	shell              *shell.Shell
	textures           map[*surface.Surface]texture
	cursorPixels       map[*surface.Surface]*cursorPixels
	uploadRects        []image.Rectangle
	imported           map[*dmabuf.Buffer]importedTexture
	colorrep           *colorrep.Manager
	colorManager       *colormanager.Manager
	colorManagement    bool
	clientHotkeys      bool
	hotkeys            hotkeyState
	plans              colorspace.Plans
	hdr                map[backend.Output]*hdrColor
	ctms               map[backend.Output]backend.Matrix
	captureTargets     map[*dmabuf.Buffer]render.ReadTarget
	dmabuf             *dmabuf.Manager
	syncobj            *syncobj.Manager
	waits              waitWatch
	importer           dmabufRenderer
	power              *outputpower.Manager
	gamma              *gammacontrol.Manager
	ctm                *ctmcontrol.Manager
	outputConfig       *outputmanagement.Manager
	alpha              *alphamodifier.Manager
	hyprSurfaces       *hyprlandsurface.Manager
	effects            *backgroundeffect.Manager
	fifo               *fifo.Manager
	timing             *committiming.Manager
	tearingHints       *tearingcontrol.Manager
	contentTypes       *contenttype.Manager
	tearing            bool
	inputMethods       *inputmethod.Manager
	outputOverrides    map[string]OutputRule
	lidClosed          bool
	windows            []*window
	stack              []*window
	interaction        *interaction
	preview            *dragPreview
	txn                *transaction
	txnStore           transaction
	holdTimer          *loop.Timer
	hidden             throttle
	holdBound          time.Duration
	holdOpening        bool
	holds              HoldStats
	awaited            []*window
	layoutDirty        bool
	dragPending        bool
	dragMovedAt        time.Duration
	releasing          bool
	arrangers          [3]wm.Arranger
	shownArranger      *wm.Arranger
	lagging            bool
	wm                 wm.State
	settings           wm.Settings
	rules              []wm.Rule
	swallow            Swallow
	procKnown          bool
	procNSDepth        int
	rulesGen           uint64
	decor              Decor
	font               *gtext.Typeface
	icons              *icontheme.Theme
	labels             map[*output.Output]map[labelKey]*label
	stops              map[render.BorderStops]render.Texture
	scenes             map[*output.Output]*scene
	dropped            map[*output.Output]int
	inherited          map[*output.Output]*loop.Timer
	inheritTimeout     time.Duration
	outputResources    []*sw.WlOutput
	tabBars            []wm.TabBar
	rings              []wm.GroupRing
	snapRects          []image.Rectangle
	binds              binds.Map
	submap             string
	held               []uint32
	armed              armedBind
	bindFired          []bindFire
	ipcBinds           []ipc.Bind
	modRelease         *modRelease
	repeat             repeat
	slow               slowKeys
	report             func(error)
	seatConfig         seat.Config
	outputRules        OutputRules
	disabled           []backend.Output
	virtualOutputs     []virtualOutput
	virtualConfig      []VirtualOutput
	mirrors            []*mirror
	toplevelDrags      []*toplevelDrag
	cast               castTarget
	castSize           image.Point
	powerSaver         bool
	powerSaverRate     int
	hooks              Hooks
	ipcEvents          IPCEvents
	changed            func()
	loop               *loop.Loop
	display            *server.Display
	nextWindow         wm.WindowID
	active             *window
	consumed           []uint32
	tokens             []activationToken
	layers             []*layer
	layerIDs           uint64
	picking            *pick
	barHover           barHover
	lastBarPress       barPress
	debugDraw          *debugOverlays
	recorders          debugRecorders
	data               *data.Manager
	drag               *dragging
	usable             map[*output.Output]image.Rectangle
	layerFocus         *shell.LayerSurface
	focusBy            wm.FocusOrigin
	hovered            *window
	hoverHeld          bool
	stateUpdates       uint64
	warps              uint64
	focusGrab          *focusGrab
	shown              map[*output.Output][]*surface.Surface
	footprints         map[*surface.Surface]footprint
	entered            []*entered
	cursor             point
	touches            map[int32]touchAt
	virtualTouchBlocks int
	tabletButtons      map[*tabletinput.Tool][]uint32
	cursors            CursorConfig
	pointers           map[cursorKey]*cursorTexture
	hwCursors          map[*output.Output]*hwCursor
	unmanaged          []*xwayland.Window
	selectionWatch     func(data.Kind, data.Source) error
	workAreaWatch      func(image.Rectangle)
	xdragOver          func(*surface.Surface, bool) error
	workArea           image.Rectangle
	xwaylandClients    func(*server.Client) bool
	blanked            map[*output.Output]bool
	lockFade           anim.Tween[float64]
	lockShown          bool
	lockSnapshots      map[*output.Output]render.TextureTarget
	idle               *idle.Manager
	extLists           []*extList
	wlrManagers        []*wlrManager
	wsManagers         []*wsManager
	captures           map[*output.Output][]*capture
	reads              map[*asyncRead]struct{}
	cursorReads        map[*cursorRead]struct{}
	offLoopWork        sync.WaitGroup
	captureSessions    []*captureSession
	windowCopies       []*copyFrame
	cursorSessions     []*cursorSession
	scannedOut         map[*dmabuf.Buffer]*scannedOut
	scannedFree        []*scannedOut
	overlays           map[*output.Output]overlaysShown
	candidates         map[*surface.Surface]*candidacy
	hdrSwitches        map[backend.Output]*hdrSwitch
	profiles           map[backend.Output]*profile
	hdrHold            time.Duration
	overlayWork        overlayWork
	lookPath           func(string) (string, error)
	execOutput         ExecOutput
	execs              loop.Serial
	cursorLoader       func(theme *xcursor.Theme, name string, px int) (xcursor.Image, error)
	cursorsLoading     map[cursorKey]struct{}
	labelWork          loop.Serial
	labelsPending      map[labelJob]struct{}
	landed             uint64
	labelSlots         map[*output.Output]map[labelSlot]labelKey
	loadedConfig       func() error
	reloading          int
	capturedPixels     uint64
	lidBusy            bool
	lidAgain           bool
	filled             map[*output.Output]filling
	constraints        map[*surface.Surface]*constraint
	constraint         *constraint
	keyInhibitors      map[*surface.Surface]*shortcutsInhibitor
	inhibiting         *shortcutsInhibitor
	escapeArms         []*escapeArm
	inputCaptures      []*inputCapture
	capturing          *activeCapture
	activations        uint32
	globalShortcuts    []*globalShortcut
	heldShortcuts      []heldShortcut
	cursorGen          uint64
	cursorOps          frame
	cursorShown        []*surface.Surface
	presence           cursorPresence
	animations         anim.Table
	clock              func() time.Duration
	frameTime          time.Duration
	closing            []*closing
	blurGen            uint64
	grain              grainTile
	jellyGen           uint64
	genieStrips        []anim.GenieStrip
	layerRules         []wm.LayerRule
	workspaceRules     []WorkspaceRule
	shownEmpty         map[wm.WorkspaceID]bool
	maskedOps          []drawOp
	blockedScratch     []image.Rectangle
	exceptScratch      []image.Rectangle
	overview           *overview
	overviewStyle      OverviewStyle
	switcherStyle      SwitcherStyle
	measuredLabel      map[measureKey]int
	switcher           *switcher
	switcherFrames     uint64
	switcherRedraws    uint64
	mru                mru
	mruPending         *window
	walk               *mruWalk
	keptBackdrop       bool
	tallied            tally
	traced             tally
	copiedBytes        atomic.Uint64
	tracedCounts       render.Counts
	gpuLosses          lgpu.Losses
	gpuResets          uint64
	reuploads          uint64
	shmSyncReads       uint64
	switches           map[*output.Output]*workspaceSwitch
	scratchSlideOf     map[*output.Output]*workspaceSwitch
	peeks              map[*output.Output]*peek
	shownScratch       map[string]wm.WorkspaceID
	drawnSwitch        *workspaceSwitch
	gooFrom, gooTo     []*window
	gestures           Gestures
	hotCorners         HotCorners
	hot                hotCornerState
	hold               dragHold
	gesture            gestureInput
	touchGesture       touchGesture
	shownWorkspace     map[string]wm.WorkspaceID
	shaders            map[string]*screenShader
	configProblems     ConfigProblems
	parked             bool
	adopting           *adoption
	noticeDismissed    bool
}

type entered struct {
	s       *surface.Surface
	outputs []*output.Output
}

func validate(cfg Config) (binds.Map, error) {
	if cfg.Report == nil {
		return binds.Map{}, errors.New("compositor: Config.Report is nil")
	}
	if cfg.ExecOutput.Stdout == nil || cfg.ExecOutput.Stderr == nil {
		return binds.Map{}, errors.New("compositor: Config.ExecOutput names no file for an exec's stdout or stderr")
	}
	if cfg.Font == nil {
		return binds.Map{}, errors.New("compositor: Config.Font is nil")
	}
	if cfg.Icons == nil {
		return binds.Map{}, errors.New("compositor: Config.Icons is nil")
	}
	if cfg.Clock == nil {
		return binds.Map{}, errors.New("compositor: Config.Clock is nil")
	}
	if !(cfg.Gestures.Distance > 0) {
		return binds.Map{}, fmt.Errorf("compositor: gesture distance %v is not positive", cfg.Gestures.Distance)
	}
	if s := cfg.Decor.DragPreview.Switch; s.Width < 1 || s.Frames < 1 {
		return binds.Map{}, fmt.Errorf("compositor: drag switch width %d and frames %d must both be at least 1", s.Width, s.Frames)
	}
	if !(cfg.HotCorners.Pressure > 0) {
		return binds.Map{}, fmt.Errorf("compositor: hot corner pressure %v is not positive", cfg.HotCorners.Pressure)
	}
	if cfg.HoldBound <= 0 {
		return binds.Map{}, fmt.Errorf("compositor: layout hold bound %v is not positive", cfg.HoldBound)
	}
	if cfg.PowerSaverRate < 0 {
		return binds.Map{}, fmt.Errorf("compositor: the power saver frame rate %d is negative", cfg.PowerSaverRate)
	}
	if cfg.HiddenFrame <= 0 {
		return binds.Map{}, fmt.Errorf("compositor: hidden surfaces' frame interval %v is not positive", cfg.HiddenFrame)
	}
	if cfg.InheritTimeout <= 0 {
		return binds.Map{}, fmt.Errorf("compositor: inherited frame timeout %v is not positive", cfg.InheritTimeout)
	}
	for _, b := range [2]render.Border{cfg.Decor.ActiveBorder, cfg.Decor.InactiveBorder} {
		if len(b.Colors()) == 0 {
			return binds.Map{}, errors.New("compositor: a border without a colour; use SolidBorder or GradientBorder")
		}
	}
	if cfg.Cursor.Size <= 0 {
		return binds.Map{}, fmt.Errorf("compositor: cursor size %d is not positive", cfg.Cursor.Size)
	}
	relevant, err := binds.Relevant(cfg.Seat.Keymap)
	if err != nil {
		return binds.Map{}, fmt.Errorf("compositor: %w", err)
	}
	m, err := binds.NewMap(cfg.Binds, relevant)
	if err != nil {
		return binds.Map{}, fmt.Errorf("compositor: %w", err)
	}
	return m, nil
}

func (c *Compositor) adopt(cfg Config, m binds.Map) {
	if e, slower := cfg.Slower[c.renderer.Speed()]; slower {
		cfg.Decor, cfg.Animations, cfg.Overview = e.Decor, e.Animations, e.Overview
	}
	c.cursors, c.settings, c.rules, c.decor, c.font = cfg.Cursor, cfg.WM, cfg.Rules, cfg.Decor, cfg.Font
	c.report, c.seatConfig, c.outputRules = cfg.Report, cfg.Seat, cfg.Outputs
	c.execOutput = cfg.ExecOutput
	c.adoptBinds(m)
	c.virtualConfig = cfg.VirtualOutputs
	c.deviceOutputs = cfg.DeviceOutputs
	c.colorManagement = cfg.ColorManagement
	c.clientHotkeys = cfg.ClientHotkeys
	c.tearing, c.icons, c.inheritTimeout, c.holdBound = cfg.Tearing, cfg.Icons, cfg.InheritTimeout, cfg.HoldBound
	c.animations, c.clock, c.layerRules, c.workspaceRules = cfg.Animations, cfg.Clock, cfg.LayerRules, cfg.WorkspaceRules
	c.swallow = cfg.Swallow
	c.syncWorkspaceRules()
	c.gestures = cfg.Gestures
	c.hotCorners = cfg.HotCorners
	c.hot.forget()
	c.hidden.every = cfg.HiddenFrame
	c.powerSaverRate = cfg.PowerSaverRate
	c.hdrHold = cfg.HDRHold
	c.capFrameRates()
	c.holdOpening = cfg.HoldOpening
	c.overviewStyle = cfg.Overview
	c.switcherStyle = cfg.Switcher
	c.rulesGen++
}

func New(l *loop.Loop, display *server.Display, b backend.Backend, cfg Config) (*Compositor, error) {
	m, err := validate(cfg)
	if err != nil {
		return nil, err
	}
	c := &Compositor{
		renderer:       b.Renderer(),
		textures:       map[*surface.Surface]texture{},
		imported:       map[*dmabuf.Buffer]importedTexture{},
		captureTargets: map[*dmabuf.Buffer]render.ReadTarget{},
		cursorPixels:   map[*surface.Surface]*cursorPixels{},
		shown:          map[*output.Output][]*surface.Surface{},
		footprints:     map[*surface.Surface]footprint{},
		touches:        map[int32]touchAt{},
		labels:         map[*output.Output]map[labelKey]*label{},
		stops:          map[render.BorderStops]render.Texture{},
		switches:       map[*output.Output]*workspaceSwitch{},
		shownWorkspace: map[string]wm.WorkspaceID{},
		scratchSlideOf: map[*output.Output]*workspaceSwitch{},
		peeks:          map[*output.Output]*peek{},
		shownEmpty:     map[wm.WorkspaceID]bool{},
		shownScratch:   map[string]wm.WorkspaceID{},
		shaders:        map[string]*screenShader{},
		scenes:         map[*output.Output]*scene{},
		hdrSwitches:    map[backend.Output]*hdrSwitch{},
		profiles:       map[backend.Output]*profile{},
		dropped:        map[*output.Output]int{},
		inherited:      map[*output.Output]*loop.Timer{},
		loop:           l,
		display:        display,
		pointers:       map[cursorKey]*cursorTexture{},
		hwCursors:      map[*output.Output]*hwCursor{},
		cursorReads:    map[*cursorRead]struct{}{},
		blanked:        map[*output.Output]bool{},
		lockFade:       anim.NewTween(0.0, anim.Lerp),
		presence:       cursorPresence{grow: anim.NewMotion(1)},
		lockSnapshots:  map[*output.Output]render.TextureTarget{},
		captures:       map[*output.Output][]*capture{},
		reads:          map[*asyncRead]struct{}{},
		scannedOut:     map[*dmabuf.Buffer]*scannedOut{},
		lookPath:       exec.LookPath,
		cursorLoader:   loadCursorImage,
		labelsPending:  map[labelJob]struct{}{},
		labelSlots:     map[*output.Output]map[labelSlot]labelKey{},
		filled:         map[*output.Output]filling{},
		constraints:    map[*surface.Surface]*constraint{},
		keyInhibitors:  map[*surface.Surface]*shortcutsInhibitor{},
		usable:         map[*output.Output]image.Rectangle{},
		nextWindow:     1,
		hooks:          cfg.Hooks,
	}
	c.adopt(cfg, m)
	if err := c.setup(l, display, b, cfg); err != nil {
		return nil, errors.Join(fmt.Errorf("compositor: %w", err), c.Close())
	}
	if err := c.adoptCursorBehavior(cfg.CursorBehavior); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	if err := c.preloadCursors(c.cursors.Size); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Compositor) setup(l *loop.Loop, display *server.Display, b backend.Backend, cfg Config) error {
	var err error
	if err := sandbox(display); err != nil {
		return err
	}
	if c.repeat.timer, err = l.AddTimer(c.repeatFired); err != nil {
		return err
	}
	if c.holdTimer, err = l.AddTimer(c.holdExpired); err != nil {
		return err
	}
	if c.slow.timer, err = l.AddTimer(c.slowKeyHeld); err != nil {
		return err
	}
	if c.hidden.timer, err = l.AddTimer(c.hiddenFrame); err != nil {
		return err
	}
	if c.waits.timer, err = l.AddTimer(c.sweepWaits); err != nil {
		return err
	}
	if c.presence.timer, err = l.AddTimer(c.cursorIdleExpired); err != nil {
		return err
	}
	if c.layout, err = output.New(display); err != nil {
		return err
	}
	if c.surfaces, err = surface.New(display, c); err != nil {
		return err
	}
	if c.shm, err = shm.New(display, l, c.renderer.Formats()); err != nil {
		return err
	}
	if err := singlepixel.New(display); err != nil {
		return err
	}
	if c.power, err = outputpower.New(display, c, c.report); err != nil {
		return err
	}
	if c.gamma, err = gammacontrol.New(display, c, c.report); err != nil {
		return err
	}
	if c.ctm, err = ctmcontrol.New(display, c, c.report); err != nil {
		return err
	}
	if c.colorrep, err = colorrep.New(display); err != nil {
		return err
	}
	c.hdr, c.ctms = map[backend.Output]*hdrColor{}, map[backend.Output]backend.Matrix{}
	if err := c.syncColorManagement(); err != nil {
		return err
	}
	if c.outputConfig, err = outputmanagement.New(display, c, c.report); err != nil {
		return err
	}
	if c.alpha, err = alphamodifier.New(display); err != nil {
		return err
	}
	if c.hyprSurfaces, err = hyprlandsurface.New(display); err != nil {
		return err
	}
	if c.effects, err = backgroundeffect.New(display); err != nil {
		return err
	}
	if err := c.effects.Offer(!c.decor.Blur.Withhold); err != nil {
		return err
	}
	if err := c.addPacing(l, display); err != nil {
		return err
	}
	if _, err := display.AddGlobal(ssb.XdgSystemBellV1Interface, bellVersion, c.bindBell); err != nil {
		return err
	}
	if _, err := display.AddGlobal(shfg.HyprlandFocusGrabManagerV1Interface, focusGrabVersion, c.bindFocusGrab); err != nil {
		return err
	}
	if imp, ok := c.renderer.(dmabufRenderer); ok {
		switch c.dmabuf, err = dmabuf.New(display, l, imp, c); {
		case errors.Is(err, gpu.ErrUnsupported):
			c.dmabuf = nil
		case err != nil:
			return err
		default:
			c.importer = imp
		}
	}
	if err := c.explicitSync(l, display); err != nil {
		return err
	}
	if c.idle, err = idle.New(display, l, c.clock, c.updateInhibition); err != nil {
		return err
	}
	if err := c.addVirtualInput(display); err != nil {
		return err
	}
	if err := c.addVirtualTouch(display); err != nil {
		return err
	}
	if err := c.addForeignToplevels(display); err != nil {
		return err
	}
	if err := c.addWorkspaces(display); err != nil {
		return err
	}
	offer, err := captureOffer(c.renderer)
	if err != nil {
		return err
	}
	if err := c.addScreencopy(display, offer); err != nil {
		return err
	}
	if err := c.addImageCapture(display, offer); err != nil {
		return err
	}
	if err := errors.Join(c.addPointerConstraints(display), c.addPointerWarp(display), c.addShortcutsInhibit(display), c.addGlobalShortcuts(display), c.addEscape(display), c.addCaptureHint(display), c.addDynamicCapture(display), c.addToplevelDrag(display), c.syncHotkeys()); err != nil {
		return err
	}
	if c.data, err = data.New(display, c); err != nil {
		return err
	}
	if c.seat, err = seat.New(display, cfg.Seat, c); err != nil {
		return err
	}
	c.leds = c.seat.Locks()
	if c.inputMethods, err = inputmethod.New(display, c.seat, c); err != nil {
		return err
	}
	if c.shell, err = shell.New(display, l, c); err != nil {
		return err
	}
	if err := b.Start(c); err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	if err := c.syncVirtualOutputs(); err != nil {
		c.report(err)
	}
	return nil
}

func (c *Compositor) Reconfigure(cfg Config) error {
	if c.parked {
		return errors.New("compositor: the session is moving to another binary")
	}
	m, err := validate(cfg)
	if err != nil {
		return err
	}
	pending, err := seat.Prepare(cfg.Seat)
	if err != nil {
		return fmt.Errorf("compositor: %w", err)
	}
	gestureErr := c.endDrive()
	c.adopt(cfg, m)
	c.outputOverrides = nil
	c.submap = ""
	c.stateChanged()
	err = errors.Join(gestureErr, c.effects.Offer(!c.decor.Blur.Withhold), c.dropDecor(), c.dropShaders(), c.seat.Reconfigure(pending), c.repeat.stop(), c.adoptCursorBehavior(cfg.CursorBehavior), c.syncColorManagement(), c.syncHotkeys(), c.applyOutputRules(), c.syncVirtualOutputs(), c.scheduleAll())
	for _, o := range c.outputs {
		c.shaderOf(o)
	}
	c.syncLEDs()
	c.captureKeymapChanged()
	if err != nil {
		c.report(fmt.Errorf("compositor: apply the new config: %w", err))
	}
	return nil
}

func (c *Compositor) dropDecor() error {
	var errs []error
	for o := range c.labels {
		errs = append(errs, c.dropLabels(o))
	}
	return errors.Join(append(errs, c.dropPointers(), c.dropStops(), c.dropGrain(), c.dropOverviewTargets(), c.dropSwitcherTargets())...)
}

func (c *Compositor) explicitSync(l *loop.Loop, display *server.Display) error {
	r, ok := c.renderer.(syncRenderer)
	if !ok || !r.NativeFences() {
		return nil
	}
	node, err := r.RenderNode()
	if errors.Is(err, gpu.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	dev, err := drm.Open(node)
	if err != nil {
		return err
	}
	timelines, err := dev.Cap(drm.CapSyncobjTimeline)
	if err != nil || timelines == 0 {
		return errors.Join(err, dev.Close())
	}
	display.OnClose(dev.Close)
	c.syncobj, err = syncobj.New(display, l, dev, r, c)
	return err
}

func (c *Compositor) Close() error {
	errs := []error{c.landInFlight(), c.stopDebugRecorders(), c.closeVirtualOutputs(), c.dropOverview(), c.endReads(), c.dropDecor(), c.dropAllClosing(), c.dropLockSnapshots(), c.dropBlurs(), c.dropShaders()}
	for _, sc := range c.scenes {
		errs = append(errs, sc.dropImage())
	}
	for _, w := range c.windows {
		errs = append(errs, c.dropJelly(w))
	}
	for b, cached := range c.imported {
		errs = append(errs, cached.tex.Destroy())
		delete(c.imported, b)
	}
	for b, t := range c.captureTargets {
		errs = append(errs, t.Destroy())
		delete(c.captureTargets, b)
	}
	if c.dmabuf != nil {
		errs = append(errs, c.dmabuf.Close())
	}
	for s := range c.textures {
		errs = append(errs, c.dropTexture(s))
	}
	for o := range c.inherited {
		errs = append(errs, c.endInherited(o))
	}
	if c.repeat.timer != nil {
		errs = append(errs, c.repeat.timer.Remove())
		c.repeat.timer = nil
	}
	if c.holdTimer != nil {
		errs = append(errs, c.holdTimer.Remove())
		c.holdTimer = nil
	}
	if c.hidden.timer != nil {
		errs = append(errs, c.hidden.timer.Remove())
		c.hidden.timer = nil
	}
	if c.waits.timer != nil {
		errs = append(errs, c.waits.timer.Remove())
		c.waits.timer = nil
	}
	return errors.Join(errs...)
}

func (c *Compositor) Seat() *seat.Seat {
	return c.seat
}

func (c *Compositor) Outputs() []*output.Output {
	return slices.Clone(c.outputs)
}

func (c *Compositor) scheduleAll() error {
	var err error
	for _, o := range c.outputs {
		if e := c.schedule(o); e != nil {
			err = errors.Join(err, e)
		}
	}
	return err
}

func (c *Compositor) schedule(o *output.Output) error {
	if !c.demanded(o) {
		c.sceneOf(o).invalidate()
		return nil
	}
	c.hintAnimationStart(o)
	if err := o.Backend().ScheduleFrame(); err != nil {
		return fmt.Errorf("compositor: schedule a frame on %s: %w", o.Backend().Name(), err)
	}
	return nil
}

func (c *Compositor) hintAnimationStart(o *output.Output) {
	hint, ok := o.Backend().(backend.WorkHinter)
	if !ok || c.sceneOf(o).animated || !c.animating(o, c.clock()) {
		return
	}
	size := o.PixelSize()
	hint.ExpectFrameWork(size.X * size.Y)
}

func hintDamage(o *output.Output, d []image.Rectangle) {
	hint, ok := o.Backend().(backend.WorkHinter)
	if !ok {
		return
	}
	pixels := 0
	for _, r := range d {
		pixels += r.Dx() * r.Dy()
	}
	hint.ExpectFrameWork(pixels * o.Scale().ToDevice(1) * o.Scale().ToDevice(1))
}
