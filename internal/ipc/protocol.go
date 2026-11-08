// Package ipc serves lys-wm's state, actions and events over a Unix socket as
// JSON lines, for lys-wmctl, bars and scripts.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-lib/wmipc"

	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/wm"
)

var kinds = []wmipc.Kind{wmipc.KindVersion, wmipc.KindOutputs, wmipc.KindWorkspaces, wmipc.KindWindows, wmipc.KindActiveWindow, wmipc.KindSubmap, wmipc.KindLayout, wmipc.KindLocks, wmipc.KindReducedMotion, wmipc.KindDispatch, wmipc.KindWatch, wmipc.KindIcon, wmipc.KindOverview, wmipc.KindScratchpads, wmipc.KindConfigProblems, wmipc.KindUpgrade, wmipc.KindCreateOutput, wmipc.KindRemoveOutput, wmipc.KindCapture, wmipc.KindLayers, wmipc.KindDevices, wmipc.KindPickWindow, wmipc.KindBinds, wmipc.KindCastTarget}

type Request struct {
	Request  wmipc.Kind  `json:"request"`
	Action   string      `json:"action,omitempty"`
	Actions  []string    `json:"actions,omitempty"`
	Snapshot bool        `json:"snapshot,omitempty"`
	Window   wm.WindowID `json:"window,omitempty"`
	Size     int         `json:"size,omitempty"`
	Binary   string      `json:"binary,omitempty"`
	DropX11  bool        `json:"drop_x11,omitempty"`
	Name     string      `json:"name,omitempty"`
	Mode     *wmipc.Mode `json:"mode,omitempty"`
	Scale    float64     `json:"scale,omitempty"`
}

type OutputSpec struct {
	Name       string
	Size       image.Point
	RefreshMHz int32
	Scale      float64
}

type CreatedOutput struct {
	Name string `json:"name"`
}

type UpgradeResult struct {
	PID     int    `json:"pid"`
	Binary  string `json:"binary"`
	Clients int    `json:"clients"`
	Windows int    `json:"windows"`
}

var ErrBadRequest = errors.New("ipc: bad request")

type iconRequest struct {
	window wm.WindowID
	size   int
}

type request struct {
	kind     wmipc.Kind
	actions  []wm.Action
	snapshot bool
	icon     iconRequest
	binary   string
	dropX11  bool
	output   OutputSpec
}

func parseRequest(line []byte) (request, error) {
	var raw Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return request{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	if !slices.Contains(kinds, raw.Request) {
		return request{}, fmt.Errorf("%w: unknown request %q (known: %s)", ErrBadRequest, raw.Request, kindList())
	}
	if (raw.Request == wmipc.KindDispatch) != ((raw.Action != "") != (len(raw.Actions) > 0)) {
		return request{}, fmt.Errorf("%w: only dispatch takes an action or a list of actions, and it needs exactly one of them", ErrBadRequest)
	}
	if raw.Snapshot && raw.Request != wmipc.KindWatch {
		return request{}, fmt.Errorf("%w: only watch takes a snapshot", ErrBadRequest)
	}
	icon := raw.Window != 0 && raw.Size > 0
	if raw.Request == wmipc.KindIcon && !icon || raw.Request != wmipc.KindIcon && (raw.Window != 0 || raw.Size != 0) {
		return request{}, fmt.Errorf("%w: only icon takes a window and a size, and it needs both, the size above 0", ErrBadRequest)
	}
	if (raw.Request == wmipc.KindUpgrade) != (raw.Binary != "") {
		return request{}, fmt.Errorf("%w: only upgrade takes a binary, and it needs one", ErrBadRequest)
	}
	output, err := parseOutput(raw)
	if err != nil {
		return request{}, err
	}
	r := request{kind: raw.Request, snapshot: raw.Snapshot, icon: iconRequest{window: raw.Window, size: raw.Size}, binary: raw.Binary, dropX11: raw.DropX11, output: output}
	if raw.Request != wmipc.KindDispatch {
		return r, nil
	}
	if raw.Action != "" {
		raw.Actions = []string{raw.Action}
	}
	r.actions = make([]wm.Action, len(raw.Actions))
	for i, text := range raw.Actions {
		a, err := wm.ParseAction(text)
		if err != nil {
			return request{}, fmt.Errorf("%w: %w", ErrBadRequest, batchError(len(raw.Actions), i, text, err))
		}
		r.actions[i] = a
	}
	return r, nil
}

func parseOutput(raw Request) (OutputSpec, error) {
	switch raw.Request {
	case wmipc.KindCreateOutput:
		m := raw.Mode
		if m == nil || m.Width <= 0 || m.Height <= 0 || m.RefreshMHz <= 0 {
			return OutputSpec{}, fmt.Errorf("%w: createoutput needs a mode with a width, a height and a refresh_mhz above 0", ErrBadRequest)
		}
		if raw.Scale < 0 {
			return OutputSpec{}, fmt.Errorf("%w: createoutput's scale %g is negative", ErrBadRequest, raw.Scale)
		}
		return OutputSpec{Name: raw.Name, Size: image.Pt(m.Width, m.Height), RefreshMHz: m.RefreshMHz, Scale: raw.Scale}, nil
	case wmipc.KindRemoveOutput:
		if raw.Name == "" || raw.Mode != nil || raw.Scale != 0 {
			return OutputSpec{}, fmt.Errorf("%w: removeoutput takes the name of the output and nothing else", ErrBadRequest)
		}
		return OutputSpec{Name: raw.Name}, nil
	}
	if raw.Name != "" || raw.Mode != nil || raw.Scale != 0 {
		return OutputSpec{}, fmt.Errorf("%w: only createoutput and removeoutput take a name, and only createoutput a mode and a scale", ErrBadRequest)
	}
	return OutputSpec{}, nil
}

func ParseMode(s string) (wmipc.Mode, error) {
	size, rate, hasRate := strings.Cut(s, "@")
	w, h, ok := strings.Cut(size, "x")
	width, errW := strconv.Atoi(w)
	height, errH := strconv.Atoi(h)
	if !ok || errW != nil || errH != nil || width <= 0 || height <= 0 {
		return wmipc.Mode{}, fmt.Errorf("ipc: mode %q is not <width>x<height>[@<hz>]", s)
	}
	m := wmipc.Mode{Width: width, Height: height}
	if !hasRate {
		return m, nil
	}
	hz, err := strconv.ParseFloat(rate, 64)
	if err != nil || !(hz > 0 && hz*1000 <= math.MaxInt32) {
		return wmipc.Mode{}, fmt.Errorf("ipc: mode %q has no refresh rate in hertz above 0 after the @", s)
	}
	m.RefreshMHz = int32(math.Round(hz * 1000))
	return m, nil
}

func kindList() string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

func RectOf(r image.Rectangle) wmipc.Rect {
	return wmipc.Rect{X: r.Min.X, Y: r.Min.Y, Width: r.Dx(), Height: r.Dy()}
}

type Output struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Rect        wmipc.Rect     `json:"rect"`
	Scale       float64        `json:"scale"`
	Mode        wmipc.Mode     `json:"mode"`
	Workspace   wm.WorkspaceID `json:"workspace"`
	Focused     bool           `json:"focused"`
	Powered     bool           `json:"powered"`
	Shader      string         `json:"shader,omitempty"`
	ShaderError string         `json:"shader_error,omitempty"`
	HDR         bool           `json:"hdr,omitempty"`
	Zoom        float64        `json:"zoom,omitempty"`
	Transform   string         `json:"transform,omitempty"`
	MirrorOf    string         `json:"mirror_of,omitempty"`
}

type Workspace struct {
	ID      wm.WorkspaceID `json:"id"`
	Name    string         `json:"name,omitempty"`
	Output  string         `json:"output"`
	Windows int            `json:"windows"`
	Visible bool           `json:"visible"`
	Focused bool           `json:"focused"`
}

type Scratchpad struct {
	Name    wm.ScratchpadName `json:"name"`
	Output  string            `json:"output"`
	Shown   bool              `json:"shown"`
	Windows int               `json:"windows"`
}

type Window struct {
	ID             wm.WindowID       `json:"id"`
	AppID          string            `json:"app_id"`
	Title          string            `json:"title"`
	PID            int32             `json:"pid"`
	Workspace      wm.WorkspaceID    `json:"workspace"`
	Scratchpad     wm.ScratchpadName `json:"scratchpad,omitempty"`
	Output         string            `json:"output"`
	Rect           wmipc.Rect        `json:"rect"`
	Floating       bool              `json:"floating"`
	Fullscreen     bool              `json:"fullscreen"`
	Maximized      bool              `json:"maximized"`
	ClientFull     bool              `json:"client_fullscreen"`
	Pinned         bool              `json:"pinned"`
	Minimized      bool              `json:"minimized"`
	Urgent         bool              `json:"urgent"`
	Unresponsive   bool              `json:"unresponsive"`
	Focused        bool              `json:"focused"`
	InhibitsKeys   bool              `json:"inhibits_shortcuts"`
	ContentType    wm.ContentType    `json:"content_type"`
	ContentSource  wm.ContentSource  `json:"content_source"`
	Icon           *Icon             `json:"icon"`
	Stack          *int              `json:"stack,omitempty"`
	SwallowedBy    wm.WindowID       `json:"swallowed_by,omitempty"`
	FocusTimestamp uint64            `json:"focus_timestamp"`
	Group          *Group            `json:"group"`
}

type Group struct {
	ID     uint64 `json:"id"`
	Layout string `json:"layout"`
	Index  int    `json:"index"`
}

func sameGroup(a, b *Group) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

type LayerLevel string

const (
	LayerBackground LayerLevel = "background"
	LayerBottom     LayerLevel = "bottom"
	LayerTop        LayerLevel = "top"
	LayerOverlay    LayerLevel = "overlay"
)

type KeyboardInteractivity string

const (
	KeyboardNone      KeyboardInteractivity = "none"
	KeyboardExclusive KeyboardInteractivity = "exclusive"
	KeyboardOnDemand  KeyboardInteractivity = "on_demand"
)

type Layer struct {
	ID        uint64                `json:"id"`
	Namespace string                `json:"namespace"`
	Layer     LayerLevel            `json:"layer"`
	Output    string                `json:"output"`
	Rect      wmipc.Rect            `json:"rect"`
	Keyboard  KeyboardInteractivity `json:"keyboard_interactivity"`
}

type CastKind string

const (
	CastNone   CastKind = "none"
	CastWindow CastKind = "window"
	CastOutput CastKind = "output"
)

type CastTarget struct {
	Kind   CastKind    `json:"kind"`
	Window wm.WindowID `json:"window,omitempty"`
	Output string      `json:"output,omitempty"`
}

type Capability string

const (
	CapKeyboard  Capability = "keyboard"
	CapPointer   Capability = "pointer"
	CapTouch     Capability = "touch"
	CapTablet    Capability = "tablet"
	CapTabletPad Capability = "tablet_pad"
)

type Device struct {
	Name         string        `json:"name"`
	Capabilities []Capability  `json:"capabilities"`
	Layout       *wmipc.Layout `json:"layout,omitempty"`
}

type Icon struct {
	Serial uint64 `json:"serial"`
	Name   string `json:"name,omitempty"`
	Sizes  []int  `json:"sizes,omitempty"`
}

func (i *Icon) serial() uint64 {
	if i == nil {
		return 0
	}
	return i.Serial
}

type IconImage struct {
	Size  int    `json:"size"`
	Scale int    `json:"scale"`
	PNG   []byte `json:"png"`
}

type Snapshot struct {
	Outputs        []Output
	Workspaces     []Workspace
	Scratchpads    []Scratchpad
	ShowDesktop    []string
	Windows        []Window
	Layers         []Layer
	CastTarget     CastTarget
	Devices        []Device
	Binds          []Bind
	MRU            []wm.WindowID
	Switcher       *Switcher
	Submap         string
	Layout         wmipc.Layout
	Locks          wmipc.Locks
	ReducedMotion  bool
	Overview       bool
	ConfigProblems ConfigProblems
}

type ConfigProblems struct {
	Rejected bool     `json:"rejected"`
	Messages []string `json:"messages"`
}

func (s Snapshot) active() (Window, bool) {
	i := slices.IndexFunc(s.Windows, func(w Window) bool { return w.Focused })
	if i < 0 {
		return Window{}, false
	}
	return s.Windows[i], true
}

func (s Snapshot) answer(k wmipc.Kind) any {
	switch k {
	case wmipc.KindVersion:
		return wmipc.Version
	case wmipc.KindOutputs:
		return nonNil(s.Outputs)
	case wmipc.KindWorkspaces:
		return nonNil(s.Workspaces)
	case wmipc.KindWindows:
		return nonNil(s.Windows)
	case wmipc.KindActiveWindow:
		if w, ok := s.active(); ok {
			return w
		}
	case wmipc.KindSubmap:
		return s.Submap
	case wmipc.KindLayout:
		return s.Layout
	case wmipc.KindLocks:
		return s.Locks
	case wmipc.KindReducedMotion:
		return s.ReducedMotion
	case wmipc.KindOverview:
		return s.Overview
	case wmipc.KindScratchpads:
		return nonNil(s.Scratchpads)
	case wmipc.KindLayers:
		return nonNil(s.Layers)
	case wmipc.KindDevices:
		return nonNil(s.Devices)
	case wmipc.KindCastTarget:
		return s.CastTarget
	case wmipc.KindBinds:
		return nonNil(s.Binds)
	case wmipc.KindConfigProblems:
		return ConfigProblems{Rejected: s.ConfigProblems.Rejected, Messages: nonNil(s.ConfigProblems.Messages)}
	}
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func batchError(n, i int, action string, err error) error {
	if n == 1 {
		return err
	}
	return fmt.Errorf("action %d (%s): %w", i, action, err)
}

type Bind struct {
	Submap      string   `json:"submap"`
	Chord       string   `json:"chord"`
	Action      string   `json:"action"`
	Description string   `json:"description"`
	Flags       []string `json:"flags"`
	LongPressMS int64    `json:"long_press_ms,omitempty"`
	CooldownMS  int64    `json:"cooldown_ms,omitempty"`
}

func BindOf(b binds.Bind) Bind {
	return Bind{
		Submap: b.Submap, Chord: b.Text, Action: b.Action.String(), Description: b.Description, Flags: b.Flags.Names(),
		LongPressMS: b.Flags.LongPress.Milliseconds(), CooldownMS: b.Flags.Cooldown.Milliseconds(),
	}
}

func sameBinds(a, b []Bind) bool {
	return slices.EqualFunc(a, b, func(x, y Bind) bool {
		return x.Submap == y.Submap && x.Chord == y.Chord && x.Action == y.Action && x.Description == y.Description &&
			x.LongPressMS == y.LongPressMS && x.CooldownMS == y.CooldownMS && slices.Equal(x.Flags, y.Flags)
	})
}

type Switcher struct {
	Selected wm.WindowID `json:"selected"`
	Title    string      `json:"title"`
}
