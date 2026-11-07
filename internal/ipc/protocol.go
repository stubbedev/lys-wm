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

	"github.com/stubbedev/lys-wm/internal/wm"
)

const Version = 1

type Kind string

const (
	KindVersion        Kind = "version"
	KindOutputs        Kind = "outputs"
	KindWorkspaces     Kind = "workspaces"
	KindWindows        Kind = "windows"
	KindActiveWindow   Kind = "activewindow"
	KindSubmap         Kind = "submap"
	KindLayout         Kind = "layout"
	KindLocks          Kind = "locks"
	KindReducedMotion  Kind = "reducedmotion"
	KindDispatch       Kind = "dispatch"
	KindWatch          Kind = "watch"
	KindIcon           Kind = "icon"
	KindOverview       Kind = "overview"
	KindConfigProblems Kind = "configproblems"
	KindUpgrade        Kind = "upgrade"
	KindCreateOutput   Kind = "createoutput"
	KindRemoveOutput   Kind = "removeoutput"
)

var kinds = []Kind{KindVersion, KindOutputs, KindWorkspaces, KindWindows, KindActiveWindow, KindSubmap, KindLayout, KindLocks, KindReducedMotion, KindDispatch, KindWatch, KindIcon, KindOverview, KindConfigProblems, KindUpgrade, KindCreateOutput, KindRemoveOutput}

type Request struct {
	Request Kind        `json:"request"`
	Action  string      `json:"action,omitempty"`
	Window  wm.WindowID `json:"window,omitempty"`
	Size    int         `json:"size,omitempty"`
	Binary  string      `json:"binary,omitempty"`
	DropX11 bool        `json:"drop_x11,omitempty"`
	Name    string      `json:"name,omitempty"`
	Mode    *Mode       `json:"mode,omitempty"`
	Scale   float64     `json:"scale,omitempty"`
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

type Response struct {
	Version int             `json:"version"`
	Error   string          `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

var ErrBadRequest = errors.New("ipc: bad request")

type iconRequest struct {
	window wm.WindowID
	size   int
}

type request struct {
	kind    Kind
	action  wm.Action
	icon    iconRequest
	binary  string
	dropX11 bool
	output  OutputSpec
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
	if (raw.Request == KindDispatch) != (raw.Action != "") {
		return request{}, fmt.Errorf("%w: only dispatch takes an action, and it needs one", ErrBadRequest)
	}
	icon := raw.Window != 0 && raw.Size > 0
	if raw.Request == KindIcon && !icon || raw.Request != KindIcon && (raw.Window != 0 || raw.Size != 0) {
		return request{}, fmt.Errorf("%w: only icon takes a window and a size, and it needs both, the size above 0", ErrBadRequest)
	}
	if (raw.Request == KindUpgrade) != (raw.Binary != "") {
		return request{}, fmt.Errorf("%w: only upgrade takes a binary, and it needs one", ErrBadRequest)
	}
	output, err := parseOutput(raw)
	if err != nil {
		return request{}, err
	}
	r := request{kind: raw.Request, icon: iconRequest{window: raw.Window, size: raw.Size}, binary: raw.Binary, dropX11: raw.DropX11, output: output}
	if raw.Request == KindDispatch {
		a, err := wm.ParseAction(raw.Action)
		if err != nil {
			return request{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		r.action = a
	}
	return r, nil
}

func parseOutput(raw Request) (OutputSpec, error) {
	switch raw.Request {
	case KindCreateOutput:
		m := raw.Mode
		if m == nil || m.Width <= 0 || m.Height <= 0 || m.RefreshMHz <= 0 {
			return OutputSpec{}, fmt.Errorf("%w: createoutput needs a mode with a width, a height and a refresh_mhz above 0", ErrBadRequest)
		}
		if raw.Scale < 0 {
			return OutputSpec{}, fmt.Errorf("%w: createoutput's scale %g is negative", ErrBadRequest, raw.Scale)
		}
		return OutputSpec{Name: raw.Name, Size: image.Pt(m.Width, m.Height), RefreshMHz: m.RefreshMHz, Scale: raw.Scale}, nil
	case KindRemoveOutput:
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

func ParseMode(s string) (Mode, error) {
	size, rate, hasRate := strings.Cut(s, "@")
	w, h, ok := strings.Cut(size, "x")
	width, errW := strconv.Atoi(w)
	height, errH := strconv.Atoi(h)
	if !ok || errW != nil || errH != nil || width <= 0 || height <= 0 {
		return Mode{}, fmt.Errorf("ipc: mode %q is not <width>x<height>[@<hz>]", s)
	}
	m := Mode{Width: width, Height: height}
	if !hasRate {
		return m, nil
	}
	hz, err := strconv.ParseFloat(rate, 64)
	if err != nil || !(hz > 0 && hz*1000 <= math.MaxInt32) {
		return Mode{}, fmt.Errorf("ipc: mode %q has no refresh rate in hertz above 0 after the @", s)
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

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func RectOf(r image.Rectangle) Rect {
	return Rect{X: r.Min.X, Y: r.Min.Y, Width: r.Dx(), Height: r.Dy()}
}

type Mode struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	RefreshMHz int32 `json:"refresh_mhz"`
}

type Output struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Rect        Rect           `json:"rect"`
	Scale       float64        `json:"scale"`
	Mode        Mode           `json:"mode"`
	Workspace   wm.WorkspaceID `json:"workspace"`
	Focused     bool           `json:"focused"`
	Powered     bool           `json:"powered"`
	Shader      string         `json:"shader,omitempty"`
	ShaderError string         `json:"shader_error,omitempty"`
	HDR         bool           `json:"hdr,omitempty"`
	Zoom        float64        `json:"zoom,omitempty"`
}

type Workspace struct {
	ID      wm.WorkspaceID `json:"id"`
	Output  string         `json:"output"`
	Windows int            `json:"windows"`
	Visible bool           `json:"visible"`
	Focused bool           `json:"focused"`
}

type Window struct {
	ID            wm.WindowID      `json:"id"`
	AppID         string           `json:"app_id"`
	Title         string           `json:"title"`
	PID           int32            `json:"pid"`
	Workspace     wm.WorkspaceID   `json:"workspace"`
	Output        string           `json:"output"`
	Rect          Rect             `json:"rect"`
	Floating      bool             `json:"floating"`
	Fullscreen    bool             `json:"fullscreen"`
	Maximized     bool             `json:"maximized"`
	ClientFull    bool             `json:"client_fullscreen"`
	Minimized     bool             `json:"minimized"`
	Urgent        bool             `json:"urgent"`
	Unresponsive  bool             `json:"unresponsive"`
	Focused       bool             `json:"focused"`
	ContentType   wm.ContentType   `json:"content_type"`
	ContentSource wm.ContentSource `json:"content_source"`
	Icon          *Icon            `json:"icon"`
	Stack         *int             `json:"stack,omitempty"`
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

type Layout struct {
	Index uint32 `json:"index"`
	Name  string `json:"name"`
}

type Locks struct {
	Caps   bool `json:"caps"`
	Num    bool `json:"num"`
	Scroll bool `json:"scroll"`
}

type Snapshot struct {
	Outputs        []Output
	Workspaces     []Workspace
	Windows        []Window
	Submap         string
	Layout         Layout
	Locks          Locks
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

func (s Snapshot) answer(k Kind) any {
	switch k {
	case KindVersion:
		return Version
	case KindOutputs:
		return nonNil(s.Outputs)
	case KindWorkspaces:
		return nonNil(s.Workspaces)
	case KindWindows:
		return nonNil(s.Windows)
	case KindActiveWindow:
		if w, ok := s.active(); ok {
			return w
		}
	case KindSubmap:
		return s.Submap
	case KindLayout:
		return s.Layout
	case KindLocks:
		return s.Locks
	case KindReducedMotion:
		return s.ReducedMotion
	case KindOverview:
		return s.Overview
	case KindConfigProblems:
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
