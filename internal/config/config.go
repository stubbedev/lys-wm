// Package config reads lys-wm's YAML config into validated types. Every
// error names the file, the line and the key.
package config

import (
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	gtext "github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/icontheme"
	"go.yaml.in/yaml/v3"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/ipc"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/session"
	"github.com/stubbedev/lys-wm/internal/wm"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

var reservedEnv = []string{"WAYLAND_DISPLAY", "DISPLAY", ipc.SocketEnv}

var sessionEnv = map[string]string{"XDG_CURRENT_DESKTOP": "lys-wm", "XDG_SESSION_TYPE": "wayland"}

//go:embed default.yaml
var Default []byte

type At[T any] struct {
	V    T
	Line int
}

func (a *At[T]) UnmarshalYAML(n *yaml.Node) error {
	a.Line = n.Line
	return n.Decode(&a.V)
}

func (*At[T]) located() {}

var locatedType = reflect.TypeFor[interface{ located() }]()

func fieldNames(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name != "" {
			fields[name] = f.Type
		}
		if opts == "inline" {
			maps.Copy(fields, fieldNames(f.Type))
		}
	}
	return fields
}

func (p *parser) unknownKeys(n *yaml.Node, t reflect.Type, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(locatedType) {
		t = t.Field(0).Type
	}
	join := func(key string) string {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	switch {
	case n.Kind == yaml.DocumentNode:
		for _, c := range n.Content {
			p.unknownKeys(c, t, path)
		}
	case n.Kind == yaml.SequenceNode && t.Kind() == reflect.Slice:
		for i, c := range n.Content {
			p.unknownKeys(c, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
		}
	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Map:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p.unknownKeys(n.Content[i+1], t.Elem(), join(n.Content[i].Value))
		}
	case n.Kind == yaml.MappingNode && t.Kind() == reflect.Struct:
		fields := fieldNames(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			ft, ok := fields[key.Value]
			if !ok {
				p.warn(key.Line, join(key.Value), "unknown key (known: %s)", strings.Join(slices.Sorted(maps.Keys(fields)), ", "))
				continue
			}
			p.unknownKeys(n.Content[i+1], ft, join(key.Value))
		}
	}
}

type rawConfig struct {
	ColorManagement bool                  `yaml:"color_management"`
	ClientHotkeys   bool                  `yaml:"client_hotkeys"`
	HiddenFrameRate *At[int]              `yaml:"hidden_frame_rate"`
	HoldOpening     bool                  `yaml:"hold_opening"`
	Outputs         []At[rawOutput]       `yaml:"outputs"`
	Input           rawInput              `yaml:"input"`
	Palette         map[string]At[string] `yaml:"palette"`
	Borders         rawBorders            `yaml:"borders"`
	Gaps            rawGaps               `yaml:"gaps"`
	Decoration      rawDecoration         `yaml:"decoration"`
	DragPreview     rawDragPreview        `yaml:"drag_preview"`
	Animations      rawAnimations         `yaml:"animations"`
	Tabs            rawTabs               `yaml:"tabs"`
	Overview        rawOverview           `yaml:"overview"`
	Autotile        rawAutotile           `yaml:"autotile"`
	Binds           []rawBind             `yaml:"binds"`
	Submaps         map[string][]rawBind  `yaml:"submaps"`
	Rules           []At[rawRule]         `yaml:"rules"`
	Env             map[string]At[string] `yaml:"env"`
	Autostart       []At[string]          `yaml:"autostart"`
	Xwayland        rawXwayland           `yaml:"xwayland"`
	GPU             *At[string]           `yaml:"gpu"`
	Tearing         bool                  `yaml:"tearing"`
	LidSuspend      *bool                 `yaml:"lid_suspend"`
	Gestures        rawGestures           `yaml:"gestures"`
	Focus           rawFocus              `yaml:"focus"`
}

type rawFocus struct {
	OnActivate *At[string] `yaml:"on_activate"`
}

type rawXwayland struct {
	Enabled     *bool `yaml:"enabled"`
	ZeroScaling *bool `yaml:"zero_scaling"`
}

type rawOutput struct {
	Match         At[string]   `yaml:"match"`
	Mode          *At[string]  `yaml:"mode"`
	Position      *At[string]  `yaml:"position"`
	Scale         *At[float64] `yaml:"scale"`
	BitDepth      *At[int]     `yaml:"bitdepth"`
	VRR           *At[string]  `yaml:"vrr"`
	Disabled      bool         `yaml:"disabled"`
	HDR           bool         `yaml:"hdr"`
	SDRBrightness *At[float64] `yaml:"sdr_brightness"`
	HDRPeak       *At[float64] `yaml:"hdr_peak"`
	Shader        *At[string]  `yaml:"shader"`
}

type rawInput struct {
	Keyboard rawKeyboard          `yaml:"keyboard"`
	Pointer  rawDevice            `yaml:"pointer"`
	Touchpad rawDevice            `yaml:"touchpad"`
	Devices  map[string]rawDevice `yaml:"devices"`
}

type rawDevice struct {
	Tap                *bool        `yaml:"tap"`
	DragLock           *bool        `yaml:"drag_lock"`
	NaturalScroll      *bool        `yaml:"natural_scroll"`
	DisableWhileTyping *bool        `yaml:"disable_while_typing"`
	MiddleEmulation    *bool        `yaml:"middle_emulation"`
	ClickMethod        *At[string]  `yaml:"click_method"`
	ScrollMethod       *At[string]  `yaml:"scroll_method"`
	AccelProfile       *At[string]  `yaml:"accel_profile"`
	AccelSpeed         *At[float64] `yaml:"accel_speed"`
	ScrollFactor       *At[float64] `yaml:"scroll_factor"`
}

type rawKeyboard struct {
	Rules       At[string] `yaml:"rules"`
	Model       At[string] `yaml:"model"`
	Layout      At[string] `yaml:"layout"`
	Variant     At[string] `yaml:"variant"`
	Options     At[string] `yaml:"options"`
	RepeatRate  *At[int]   `yaml:"repeat_rate"`
	RepeatDelay *At[int]   `yaml:"repeat_delay"`
}

type rawBorders struct {
	Width    *At[int]       `yaml:"width"`
	Active   *At[rawBorder] `yaml:"active"`
	Inactive *At[rawBorder] `yaml:"inactive"`
}

type rawBorder struct {
	Colors []At[string] `yaml:"colors"`
	Angle  float64      `yaml:"angle"`
}

func (b *rawBorder) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		b.Colors = []At[string]{{V: n.Value, Line: n.Line}}
		return nil
	}
	type plain rawBorder
	return n.Decode((*plain)(b))
}

type rawAnimations struct {
	Enabled       *bool                   `yaml:"enabled"`
	ReducedMotion *bool                   `yaml:"reduced_motion"`
	Curves        map[string]At[rawCurve] `yaml:"curves"`
	Events        map[string]At[rawEvent] `yaml:"events"`
}

type rawCurve struct {
	Bezier *At[[]float64] `yaml:"bezier"`
	Spring *rawSpring     `yaml:"spring"`
}

type rawSpring struct {
	Stiffness float64  `yaml:"stiffness"`
	Damping   float64  `yaml:"damping"`
	Mass      *float64 `yaml:"mass"`
}

type rawEvent struct {
	Enabled  *bool       `yaml:"enabled"`
	Curve    *At[string] `yaml:"curve"`
	Duration *At[int]    `yaml:"duration"`
	Style    *At[string] `yaml:"style"`
}

type rawDecoration struct {
	Rounding          *At[int]     `yaml:"rounding"`
	ActiveOpacity     *At[float64] `yaml:"active_opacity"`
	InactiveOpacity   *At[float64] `yaml:"inactive_opacity"`
	FullscreenOpacity *At[float64] `yaml:"fullscreen_opacity"`
	DimInactive       *bool        `yaml:"dim_inactive"`
	DimStrength       *At[float64] `yaml:"dim_strength"`
	Shadow            rawShadow    `yaml:"shadow"`
	Blur              rawBlur      `yaml:"blur"`
}

type rawDragPreview struct {
	Enabled      *bool        `yaml:"enabled"`
	Opacity      *At[float64] `yaml:"opacity"`
	Blur         *bool        `yaml:"blur"`
	SnapDistance *At[int]     `yaml:"snap_distance"`
}

type rawBlur struct {
	Enabled   *bool        `yaml:"enabled"`
	Size      *At[int]     `yaml:"size"`
	Passes    *At[int]     `yaml:"passes"`
	Layers    []At[string] `yaml:"layers"`
	XRay      *bool        `yaml:"xray"`
	LiveTiled *bool        `yaml:"live_tiled"`
	Mica      rawMica      `yaml:"mica"`
	rawLook   `yaml:",inline"`
}

type rawLook struct {
	Tint       *At[string]    `yaml:"tint"`
	Saturation *At[float64]   `yaml:"saturation"`
	Brightness *At[float64]   `yaml:"brightness"`
	Contrast   *At[float64]   `yaml:"contrast"`
	Grain      *At[float64]   `yaml:"grain"`
	Refraction *rawRefraction `yaml:"refraction"`
	Specular   *rawSpecular   `yaml:"specular"`
}

type rawRefraction struct {
	Bezel  *At[int] `yaml:"bezel"`
	Amount *At[int] `yaml:"amount"`
}

type rawSpecular struct {
	Strength *At[float64] `yaml:"strength"`
	Width    *At[int]     `yaml:"width"`
}

type rawMica struct {
	Enabled *bool       `yaml:"enabled"`
	Size    *At[int]    `yaml:"size"`
	Passes  *At[int]    `yaml:"passes"`
	Tint    *At[string] `yaml:"tint"`
}

type rawShadow struct {
	Enabled       *bool        `yaml:"enabled"`
	Range         *At[int]     `yaml:"range"`
	RenderPower   *At[int]     `yaml:"render_power"`
	Sharp         *bool        `yaml:"sharp"`
	Offset        *At[sides]   `yaml:"offset"`
	Scale         *At[float64] `yaml:"scale"`
	Color         *At[string]  `yaml:"color"`
	ColorInactive *At[string]  `yaml:"color_inactive"`
}

type rawGaps struct {
	Inner *At[int]   `yaml:"inner"`
	Outer *At[sides] `yaml:"outer"`
}

type sides []int

func (s *sides) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var v int
		if err := n.Decode(&v); err != nil {
			return err
		}
		*s = sides{v}
		return nil
	}
	return n.Decode((*[]int)(s))
}

type rawTabColors struct {
	Background At[string] `yaml:"background"`
	Text       At[string] `yaml:"text"`
}

type rawOverview struct {
	Blur *bool        `yaml:"blur"`
	Dim  *At[float64] `yaml:"dim"`
}

type rawTabs struct {
	Height      *At[int]      `yaml:"height"`
	Padding     *At[int]      `yaml:"padding"`
	TextHeight  *At[int]      `yaml:"text_height"`
	TextPadding *At[int]      `yaml:"text_padding"`
	Font        *At[string]   `yaml:"font"`
	IconTheme   *At[string]   `yaml:"icon_theme"`
	Active      *rawTabColors `yaml:"active"`
	Inactive    *rawTabColors `yaml:"inactive"`
	Urgent      *rawTabColors `yaml:"urgent"`
}

type rawAutotile struct {
	Enabled    *bool    `yaml:"enabled"`
	Ephemeral  *bool    `yaml:"ephemeral"`
	MinTrigger *At[int] `yaml:"min_trigger"`
	Divisor    *At[int] `yaml:"divisor"`
}

type rawBind struct {
	Chord  At[string]   `yaml:"chord"`
	Action At[string]   `yaml:"action"`
	Flags  []At[string] `yaml:"flags"`
}

type rawGestures struct {
	Swipe    []rawGesture `yaml:"swipe"`
	Pinch    []rawGesture `yaml:"pinch"`
	Hold     []rawGesture `yaml:"hold"`
	Distance *At[float64] `yaml:"distance"`
}

type rawGesture struct {
	Fingers   At[int]     `yaml:"fingers"`
	Modifiers *At[string] `yaml:"modifiers"`
	Direction *At[string] `yaml:"direction"`
	Action    *At[string] `yaml:"action"`
	Drive     *At[string] `yaml:"drive"`
}

type rawRule struct {
	AppID      *At[string]  `yaml:"app_id"`
	Title      *At[string]  `yaml:"title"`
	Tag        *At[string]  `yaml:"tag"`
	Float      bool         `yaml:"float"`
	Tile       bool         `yaml:"tile"`
	Size       *At[string]  `yaml:"size"`
	Tearing    *bool        `yaml:"tearing"`
	Opacity    *At[float64] `yaml:"opacity"`
	DimAround  bool         `yaml:"dim_around"`
	Blur       *bool        `yaml:"blur"`
	BlurLook   rawLook      `yaml:"blur_look"`
	Mica       *bool        `yaml:"mica"`
	OnActivate *At[string]  `yaml:"on_activate"`
}

type Error struct {
	File string
	Line int
	Key  string
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%d: %s: %v", e.File, e.Line, e.Key, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

type Bind struct {
	Submap string
	Chord  string
	Line   int
	Action wm.Action
	Flags  binds.Flags
}

type Config struct {
	layered
	File            string
	Warnings        []error
	Keyboard        xkb.Names
	RepeatRate      int32
	RepeatDelay     int32
	Input           session.InputConfig
	Outputs         compositor.OutputRules
	ColorManagement bool
	ClientHotkeys   bool
	HiddenFrame     time.Duration
	HoldOpening     bool
	Decor           compositor.Decor
	Font            string
	IconTheme       string
	WM              wm.Settings
	Binds           []Bind
	Rules           []wm.Rule
	Env             [][2]string
	Autostart       []string
	Xwayland        bool
	ZeroScaling     bool
	GPU             string
	Tearing         bool
	LidSuspend      bool
	Animations      anim.Table
	BlurLayers      []string
	Gestures        compositor.Gestures
	Overview        compositor.OverviewStyle
	Slower          map[render.Speed]compositor.Effects
}

const accentName = "accent"

var mocha = map[string]render.Color{
	"rosewater": rgb(0xf5e0dc), "flamingo": rgb(0xf2cdcd), "pink": rgb(0xf5c2e7), "mauve": rgb(0xcba6f7),
	"red": rgb(0xf38ba8), "maroon": rgb(0xeba0ac), "peach": rgb(0xfab387), "yellow": rgb(0xf9e2af),
	"green": rgb(0xa6e3a1), "teal": rgb(0x94e2d5), "sky": rgb(0x89dceb), "sapphire": rgb(0x74c7ec),
	"blue": rgb(0x89b4fa), "lavender": rgb(0xb4befe), "text": rgb(0xcdd6f4), "subtext1": rgb(0xbac2de),
	"subtext0": rgb(0xa6adc8), "overlay2": rgb(0x9399b2), "overlay1": rgb(0x7f849c), "overlay0": rgb(0x6c7086),
	"surface2": rgb(0x585b70), "surface1": rgb(0x45475a), "surface0": rgb(0x313244), "base": rgb(0x1e1e2e),
	"mantle": rgb(0x181825), "crust": rgb(0x11111b),
}

func rgb(v uint32) render.Color {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return render.Color{R: b[1], G: b[2], B: b[3], A: 0xff}
}

type parser struct {
	layered
	palette  map[string]render.Color
	errs     []error
	warnings []error
}

func (p *parser) warn(line int, key string, format string, args ...any) {
	file, at := p.where(line)
	p.warnings = append(p.warnings, &Error{File: file, Line: at, Key: key, Err: fmt.Errorf(format, args...)})
}

func (p *parser) fail(line int, key string, format string, args ...any) {
	file, at := p.where(line)
	p.errs = append(p.errs, &Error{File: file, Line: at, Key: key, Err: fmt.Errorf(format, args...)})
}

var cardName = regexp.MustCompile(`^card[0-9]+$`)

func (p *parser) card(a At[string]) string {
	name := strings.TrimPrefix(a.V, "/dev/dri/")
	if !cardName.MatchString(name) {
		p.fail(a.Line, "gpu", "%q is not a DRM card such as card1 or /dev/dri/card1", a.V)
	}
	return name
}

var xkbItem = regexp.MustCompile(`^[^\s,]+$`)

func (p *parser) xkbList(a At[string], key string, blanks bool) []string {
	if a.V == "" {
		return nil
	}
	items := strings.Split(a.V, ",")
	for _, item := range items {
		if !xkbItem.MatchString(item) && (!blanks || item != "") {
			p.fail(a.Line, key, "%q is not a comma-separated list of XKB names without spaces", a.V)
			return nil
		}
	}
	return items
}

func (p *parser) xkbName(a At[string], key string) {
	if a.V != "" && !xkbItem.MatchString(a.V) {
		p.fail(a.Line, key, "%q is not one XKB name without spaces or commas", a.V)
	}
}

func (p *parser) keyboard(r rawKeyboard) xkb.Names {
	p.xkbName(r.Rules, "input.keyboard.rules")
	p.xkbName(r.Model, "input.keyboard.model")
	layouts := p.xkbList(r.Layout, "input.keyboard.layout", false)
	variants := p.xkbList(r.Variant, "input.keyboard.variant", true)
	p.xkbList(r.Options, "input.keyboard.options", false)
	if len(layouts) > 0 && len(variants) > len(layouts) {
		p.fail(r.Variant.Line, "input.keyboard.variant", "%q names %d variants for %d layouts", r.Variant.V, len(variants), len(layouts))
	}
	return xkb.Names{Rules: r.Rules.V, Model: r.Model.V, Layout: r.Layout.V, Variant: r.Variant.V, Options: r.Options.V}
}

func (p *parser) iconTheme(a At[string]) string {
	if strings.ContainsAny(a.V, "/\x00") || a.V == "." || a.V == ".." {
		p.fail(a.Line, "tabs.icon_theme", "%q is not an icon theme's directory name", a.V)
	}
	return a.V
}

func parseHex(s string) (render.Color, bool) {
	digits, ok := strings.CutPrefix(s, "#")
	if !ok {
		return render.Color{}, false
	}
	b, err := hex.DecodeString(digits)
	switch {
	case err != nil:
		return render.Color{}, false
	case len(b) == 3:
		return render.Color{R: b[0], G: b[1], B: b[2], A: 0xff}, true
	case len(b) == 4:
		return render.Color{R: b[0], G: b[1], B: b[2], A: b[3]}, true
	}
	return render.Color{}, false
}

func (p *parser) color(a At[string], key string, fallback render.Color) render.Color {
	if c, ok := p.palette[a.V]; ok {
		return c
	}
	if c, ok := parseHex(a.V); ok {
		return c
	}
	p.fail(a.Line, key, "%q is neither a palette colour nor #rrggbb[aa]", a.V)
	return fallback
}

func (p *parser) optColor(a *At[string], key string, fallback render.Color) render.Color {
	if a == nil {
		return fallback
	}
	return p.color(*a, key, fallback)
}

func (p *parser) atLeast(a *At[int], key string, fallback, lo int, rule string) int {
	if a == nil {
		return fallback
	}
	if a.V < lo {
		p.fail(a.Line, key, "%d is not %s", a.V, rule)
		return fallback
	}
	return a.V
}

func (p *parser) positive(a *At[int], key string, fallback int) int {
	return p.atLeast(a, key, fallback, 1, "a positive number")
}

func (p *parser) nonNegative(a *At[int], key string, fallback int) int {
	return p.atLeast(a, key, fallback, 0, "zero or more")
}

func (p *parser) repeat(a *At[int], key string, fallback int32) int32 {
	if a == nil {
		return fallback
	}
	if a.V < 0 || a.V > math.MaxInt32 {
		p.fail(a.Line, key, "%d is outside 0 to %d", a.V, math.MaxInt32)
		return fallback
	}
	return int32(a.V)
}

func (p *parser) curve(name string, at At[rawCurve]) anim.Shape {
	key := "animations.curves." + name
	r := at.V
	switch {
	case (r.Bezier == nil) == (r.Spring == nil):
		p.fail(at.Line, key, "give exactly one of bezier and spring")
	case r.Bezier != nil:
		v := r.Bezier.V
		if len(v) != 4 {
			p.fail(r.Bezier.Line, key+".bezier", "%v is not [x1, y1, x2, y2]", v)
			return nil
		}
		b, err := anim.NewBezier(v[0], v[1], v[2], v[3])
		if err != nil {
			p.fail(r.Bezier.Line, key+".bezier", "%v", err)
			return nil
		}
		return b
	default:
		mass := 1.0
		if r.Spring.Mass != nil {
			mass = *r.Spring.Mass
		}
		s, err := anim.NewSpring(r.Spring.Stiffness, r.Spring.Damping, mass)
		if err != nil {
			p.fail(at.Line, key+".spring", "%v", err)
			return nil
		}
		return s
	}
	return nil
}

func (p *parser) overview(r rawOverview, style compositor.OverviewStyle) compositor.OverviewStyle {
	if r.Blur != nil {
		style.Blur = *r.Blur
	}
	if r.Dim != nil {
		if r.Dim.V < 0 || r.Dim.V > 1 {
			p.fail(r.Dim.Line, "overview.dim", "%v is not between 0 and 1", r.Dim.V)
		} else {
			style.Dim = float32(r.Dim.V)
		}
	}
	return style
}

func (p *parser) animations(r rawAnimations, motion map[anim.Event]anim.Setting) anim.Table {
	shapes := anim.Builtins()
	for _, name := range slices.Sorted(maps.Keys(r.Curves)) {
		at := r.Curves[name]
		if _, ok := shapes[name]; ok {
			p.fail(at.Line, "animations.curves."+name, "is a built-in curve")
			continue
		}
		if s := p.curve(name, at); s != nil {
			shapes[name] = s
		}
	}
	settings := map[anim.Event]anim.Setting{}
	lines := map[anim.Event]int{}
	for _, name := range slices.Sorted(maps.Keys(r.Events)) {
		at := r.Events[name]
		key := "animations.events." + name
		e, ok := anim.EventNamed(name)
		if !ok {
			known := make([]string, 0, len(anim.Events()))
			for _, e := range anim.Events() {
				known = append(known, e.String())
			}
			p.warn(at.Line, key, "unknown event (known: %s)", strings.Join(known, ", "))
			continue
		}
		lines[e] = at.Line
		s := anim.Setting{Enabled: at.V.Enabled}
		if c := at.V.Curve; c != nil {
			if s.Shape, ok = shapes[c.V]; !ok {
				p.fail(c.Line, key+".curve", "%q is not a curve (known: %s)", c.V, strings.Join(slices.Sorted(maps.Keys(shapes)), ", "))
				continue
			}
		}
		if st := at.V.Style; st != nil {
			style, err := anim.ParseStyle(st.V)
			if err != nil {
				p.fail(st.Line, key+".style", "%v", err)
				continue
			}
			s.Style = &style
		}
		if d := at.V.Duration; d != nil {
			if d.V <= 0 {
				p.fail(d.Line, key+".duration", "%d is not a positive number of milliseconds", d.V)
				continue
			}
			ms := time.Duration(d.V) * time.Millisecond
			s.Duration = &ms
		}
		settings[e] = s
	}
	enabled := r.Enabled == nil || *r.Enabled
	resolve := func(s map[anim.Event]anim.Setting) (anim.Table, map[anim.Event]error) {
		return anim.Resolve(enabled, s)
	}
	if enabled && r.ReducedMotion != nil && *r.ReducedMotion {
		resolve = anim.ResolveReduced
	}
	table, errs := resolve(anim.Over(settings, motion))
	for _, e := range anim.Events() {
		if err := errs[e]; err != nil {
			p.fail(lines[e], "animations.events."+e.String(), "%v", err)
		}
	}
	return table
}

func (p *parser) dragPreview(r rawDragPreview, def compositor.DragPreview) compositor.DragPreview {
	out := def
	if r.Enabled != nil {
		out.Enabled = *r.Enabled
	}
	if r.Blur != nil {
		out.Blur = *r.Blur
	}
	out.Opacity = p.opacity(r.Opacity, "drag_preview.opacity", def.Opacity)
	out.SnapDistance = p.positive(r.SnapDistance, "drag_preview.snap_distance", def.SnapDistance)
	return out
}

func (p *parser) opacity(a *At[float64], key string, fallback float32) float32 {
	if v := p.between(a, key, 0, 1, false); v != nil {
		return float32(*v)
	}
	return fallback
}

func (p *parser) shadow(r rawShadow, def compositor.Shadow) compositor.Shadow {
	s := def
	if r.Enabled != nil {
		s.Enabled = *r.Enabled
	}
	s.Sharp = r.Sharp != nil && *r.Sharp
	s.Range = p.nonNegative(r.Range, "decoration.shadow.range", def.Range)
	s.Power = p.atLeast(r.RenderPower, "decoration.shadow.render_power", def.Power, 1, "from 1 to 4")
	if s.Power > 4 {
		p.fail(r.RenderPower.Line, "decoration.shadow.render_power", "%d is not from 1 to 4", s.Power)
	}
	if r.Offset != nil {
		if v := r.Offset.V; len(v) == 2 {
			s.Offset = image.Pt(v[0], v[1])
		} else {
			p.fail(r.Offset.Line, "decoration.shadow.offset", "%v is not [x, y]", v)
		}
	}
	if v := p.between(r.Scale, "decoration.shadow.scale", 0, 1, true); v != nil {
		s.Scale = *v
	}
	s.Active = p.optColor(r.Color, "decoration.shadow.color", def.Active)
	inactive := def.Inactive
	if r.Color != nil {
		inactive = s.Active
	}
	s.Inactive = p.optColor(r.ColorInactive, "decoration.shadow.color_inactive", inactive)
	return s
}

func (p *parser) border(a *At[rawBorder], key string, fallback compositor.Border) compositor.Border {
	if a == nil {
		return fallback
	}
	colors := make([]render.Color, 0, len(a.V.Colors))
	for i, c := range a.V.Colors {
		k := key
		if len(a.V.Colors) > 1 {
			k = fmt.Sprintf("%s.colors[%d]", key, i)
		}
		colors = append(colors, p.color(c, k, render.Color{}))
	}
	if len(colors) == 1 {
		return compositor.SolidBorder(colors[0])
	}
	b, err := compositor.GradientBorder(colors, a.V.Angle)
	if err != nil {
		p.fail(a.Line, key, "%v", err)
		return fallback
	}
	return b
}

func (p *parser) hiddenFrame(a *At[int]) time.Duration {
	rate := p.positive(a, "hidden_frame_rate", int(time.Second/compositor.DefaultHiddenFrame))
	return time.Second / time.Duration(min(rate, int(time.Second)))
}

func (p *parser) blurPasses(a *At[int], fallback int) int {
	v := p.positive(a, "decoration.blur.passes", fallback)
	if v > 4 {
		p.fail(a.Line, "decoration.blur.passes", "%d is not from 1 to 4", v)
		return fallback
	}
	return v
}

const (
	defaultBezel      = 16
	defaultRefraction = 8
	defaultSpecular   = 0.6
)

func (p *parser) tint(a *At[string], key string) color.RGBA {
	if a == nil {
		return color.RGBA{}
	}
	return color.RGBA(p.color(*a, key, render.Color{}).Premultiply())
}

func (p *parser) look(r rawLook, key string) wm.LookOverride {
	factor := func(a *At[float64], name string) *float32 {
		v := p.between(a, key+"."+name, 0, 4, false)
		if v == nil {
			return nil
		}
		s := float32(*v - 1)
		return &s
	}
	strength := func(a *At[float64], name string) *float32 {
		v := p.between(a, key+"."+name, 0, 1, false)
		if v == nil {
			return nil
		}
		s := float32(*v)
		return &s
	}
	o := wm.LookOverride{
		SaturationShift: factor(r.Saturation, "saturation"),
		BrightnessShift: factor(r.Brightness, "brightness"),
		ContrastShift:   factor(r.Contrast, "contrast"),
		Grain:           strength(r.Grain, "grain"),
	}
	if r.Tint != nil {
		t := p.tint(r.Tint, key+".tint")
		o.Tint = &t
	}
	if r.Refraction != nil {
		o.Refraction = &wm.Refraction{
			Bezel:  p.nonNegative(r.Refraction.Bezel, key+".refraction.bezel", defaultBezel),
			Amount: p.nonNegative(r.Refraction.Amount, key+".refraction.amount", defaultRefraction),
		}
	}
	if r.Specular != nil {
		s := wm.Specular{Strength: defaultSpecular, Width: p.positive(r.Specular.Width, key+".specular.width", 1)}
		if v := strength(r.Specular.Strength, "specular.strength"); v != nil {
			s.Strength = *v
		}
		o.Specular = &s
	}
	return o
}

func (p *parser) micaPasses(a *At[int]) int {
	v := p.positive(a, "decoration.blur.mica.passes", 0)
	if v > 4 {
		p.fail(a.Line, "decoration.blur.mica.passes", "%d is not from 1 to 4", v)
		return 0
	}
	return v
}

func (p *parser) gaps(r rawGaps, def wm.Gaps) wm.Gaps {
	g := wm.Gaps{Inner: p.nonNegative(r.Inner, "gaps.inner", def.Inner), Outer: def.Outer}
	if r.Outer == nil {
		return g
	}
	v := r.Outer.V
	if len(v) == 1 {
		v = sides{v[0], v[0], v[0], v[0]}
	}
	if len(v) != 4 || slices.Min(v) < 0 {
		p.fail(r.Outer.Line, "gaps.outer", "%v is not zero or more, or a list of four such numbers (top, right, bottom, left)", r.Outer.V)
		return g
	}
	g.Outer = wm.Insets{Top: v[0], Right: v[1], Bottom: v[2], Left: v[3]}
	return g
}

func parsePair(s string) (image.Point, bool) {
	if len(s) < 3 {
		return image.Point{}, false
	}
	i := strings.IndexByte(s[1:], 'x')
	if i < 0 {
		return image.Point{}, false
	}
	x, errX := strconv.Atoi(s[:i+1])
	y, errY := strconv.Atoi(s[i+2:])
	return image.Pt(x, y), errX == nil && errY == nil
}

func (p *parser) mode(a *At[string], key string) compositor.ModePolicy {
	switch a.V {
	case "preferred":
		return compositor.PreferredMode{}
	case "highres":
		return compositor.HighResMode{}
	}
	size, rate, hasRate := strings.Cut(a.V, "@")
	dims, ok := parsePair(size)
	hz, err := strconv.ParseFloat(rate, 64)
	if !ok || dims.X <= 0 || dims.Y <= 0 || (hasRate && (err != nil || !(hz > 0 && hz*1000 <= math.MaxInt32))) {
		p.fail(a.Line, key, "%q is not preferred, highres or <width>x<height>[@<hz>]", a.V)
		return nil
	}
	return compositor.ExactMode{Size: dims, RefreshMHz: int32(math.Round(hz * 1000))}
}

const (
	outputFallback = "*"
	minLuminance   = 1
	maxLuminance   = 10000
)

func (p *parser) output(i int, r rawOutput) compositor.OutputRule {
	key := fmt.Sprintf("outputs[%d]", i)
	rule := compositor.OutputRule{Disabled: r.Disabled}
	switch desc, isDesc := strings.CutPrefix(r.Match.V, "desc:"); {
	case r.Match.V == "":
		p.fail(r.Match.Line, key+".match", "needs a connector name, desc:<description> or %q", outputFallback)
	case r.Match.V == outputFallback:
	case isDesc:
		rule.Description = desc
	default:
		rule.Name = r.Match.V
	}
	if r.Mode != nil {
		rule.Mode = p.mode(r.Mode, key+".mode")
	}
	if r.Position != nil && r.Position.V != "auto" {
		at, ok := parsePair(r.Position.V)
		if !ok {
			p.fail(r.Position.Line, key+".position", "%q is not <x>x<y>", r.Position.V)
		} else {
			rule.Position = &at
		}
	}
	if r.Scale != nil {
		if s, err := output.ParseScale(r.Scale.V); err != nil {
			p.fail(r.Scale.Line, key+".scale", "%v", err)
		} else {
			rule.Scale = s
		}
	}
	if r.BitDepth != nil {
		switch r.BitDepth.V {
		case 8:
			rule.Depth = backend.Depth8
		case 10:
			rule.Depth = backend.Depth10
		default:
			p.fail(r.BitDepth.Line, key+".bitdepth", "%d is not 8 or 10", r.BitDepth.V)
		}
	}
	if v := choice(p, r.VRR, key+".vrr", vrrPolicies); v != nil {
		rule.VRR = *v
	}
	if r.Shader != nil {
		rule.Shader = p.shaderPath(*r.Shader, key+".shader")
	}
	rule.HDR = r.HDR
	if v := p.between(r.SDRBrightness, key+".sdr_brightness", minLuminance, maxLuminance, false); v != nil {
		rule.SDRBrightness = *v
	}
	if v := p.between(r.HDRPeak, key+".hdr_peak", minLuminance, maxLuminance, false); v != nil {
		rule.HDRPeak = *v
	}
	return rule
}

func (p *parser) shaderPath(a At[string], key string) string {
	if a.V == "" {
		p.fail(a.Line, key, "an empty path; leave the key out for no shader")
		return ""
	}
	if rest, ok := strings.CutPrefix(a.V, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			p.fail(a.Line, key, "%q: %v", a.V, err)
			return ""
		}
		return filepath.Join(home, rest)
	}
	if filepath.IsAbs(a.V) {
		return a.V
	}
	file, _ := p.where(a.Line)
	return filepath.Join(filepath.Dir(file), a.V)
}

var (
	clickMethods     = map[string]session.ClickMethod{"none": session.ClickNone, "button_areas": session.ClickButtonAreas, "clickfinger": session.ClickFinger}
	scrollMethods    = map[string]session.ScrollMethod{"none": session.ScrollNone, "two_finger": session.ScrollTwoFinger, "edge": session.ScrollEdge, "on_button_down": session.ScrollOnButtonDown}
	accelProfiles    = map[string]session.AccelProfile{"flat": session.AccelFlat, "adaptive": session.AccelAdaptive}
	vrrPolicies      = map[string]compositor.VRRPolicy{"off": compositor.VRROff, "on": compositor.VRROn, "fullscreen": compositor.VRRFullscreen}
	activations      = map[string]wm.ActivatePolicy{"focus": wm.ActivateFocus, "urgent": wm.ActivateUrgent, "ignore": wm.ActivateIgnore}
	focusActivations = map[string]wm.ActivatePolicy{"focus": wm.ActivateFocus, "urgent": wm.ActivateUrgent}
)

func choice[T any](p *parser, a *At[string], key string, names map[string]T) *T {
	if a == nil {
		return nil
	}
	v, ok := names[a.V]
	if !ok {
		p.fail(a.Line, key, "%q is not one of %s", a.V, strings.Join(slices.Sorted(maps.Keys(names)), ", "))
		return nil
	}
	return &v
}

func (p *parser) between(a *At[float64], key string, lo, hi float64, open bool) *float64 {
	if a == nil {
		return nil
	}
	if a.V > hi || a.V < lo || (open && a.V == lo) || math.IsNaN(a.V) {
		bound := "from"
		if open {
			bound = "above"
		}
		p.fail(a.Line, key, "%g is not %s %g to %g", a.V, bound, lo, hi)
		return nil
	}
	return &a.V
}

func (p *parser) device(r rawDevice, key string) session.DeviceConfig {
	return session.DeviceConfig{
		Tap: r.Tap, DragLock: r.DragLock, NaturalScroll: r.NaturalScroll,
		DisableWhileTyping: r.DisableWhileTyping, MiddleEmulation: r.MiddleEmulation,
		ClickMethod:  choice(p, r.ClickMethod, key+".click_method", clickMethods),
		ScrollMethod: choice(p, r.ScrollMethod, key+".scroll_method", scrollMethods),
		AccelProfile: choice(p, r.AccelProfile, key+".accel_profile", accelProfiles),
		AccelSpeed:   p.between(r.AccelSpeed, key+".accel_speed", -1, 1, false),
		ScrollFactor: p.between(r.ScrollFactor, key+".scroll_factor", 0, 100, true),
	}
}

func (p *parser) input(r rawInput) session.InputConfig {
	c := session.InputConfig{Pointer: p.device(r.Pointer, "input.pointer"), Touchpad: p.device(r.Touchpad, "input.touchpad")}
	if len(r.Devices) > 0 {
		c.Devices = make(map[string]session.DeviceConfig, len(r.Devices))
	}
	for _, name := range slices.Sorted(maps.Keys(r.Devices)) {
		c.Devices[name] = p.device(r.Devices[name], fmt.Sprintf("input.devices.%q", name))
	}
	return c
}

func (p *parser) tabColors(r *rawTabColors, key string, fallback compositor.TabColors) compositor.TabColors {
	if r == nil {
		return fallback
	}
	return compositor.TabColors{Background: p.color(r.Background, key+".background", fallback.Background), Text: p.color(r.Text, key+".text", fallback.Text)}
}

func (p *parser) bind(submap string, i int, r rawBind, submaps map[string][]rawBind) Bind {
	key := fmt.Sprintf("binds[%d]", i)
	if submap != "" {
		key = fmt.Sprintf("submaps.%s[%d]", submap, i)
	}
	b := Bind{Submap: submap, Chord: r.Chord.V, Line: r.Chord.Line}
	if r.Chord.V == "" {
		p.fail(r.Chord.Line, key+".chord", "is empty")
	}
	a, err := wm.ParseAction(r.Action.V)
	if err != nil {
		p.fail(r.Action.Line, key+".action", "%v", err)
	}
	if sm, ok := a.(wm.SubmapAction); ok && sm.Name != "" {
		if _, defined := submaps[sm.Name]; !defined {
			p.fail(r.Action.Line, key+".action", "submap %q is not defined under submaps", sm.Name)
		}
	}
	b.Action = a
	for _, f := range r.Flags {
		switch f.V {
		case "locked":
			b.Flags.Locked = true
		case "repeating":
			b.Flags.Repeating = true
		default:
			p.fail(f.Line, key+".flags", "unknown flag %q (locked, repeating)", f.V)
		}
	}
	return b
}

var swipeDirections = map[string]binds.GestureDirection{
	"left": binds.SwipeLeft, "right": binds.SwipeRight, "up": binds.SwipeUp, "down": binds.SwipeDown,
	"horizontal": binds.SwipeHorizontal, "vertical": binds.SwipeVertical,
}

var pinchDirections = map[string]binds.GestureDirection{"in": binds.PinchIn, "out": binds.PinchOut, "both": binds.PinchBoth}

var gestureDrives = map[string]binds.Drive{"workspaces": binds.DriveWorkspaces, "zoom": binds.DriveZoom, "overview": binds.DriveOverview}

func (p *parser) gesture(kind backend.Gesture, name string, minFingers, i int, r rawGesture, directions map[string]binds.GestureDirection) (binds.GestureBind, bool) {
	key := fmt.Sprintf("gestures.%s[%d]", name, i)
	failures := len(p.errs)
	b := binds.GestureBind{Kind: kind}
	if r.Fingers.V < minFingers {
		p.fail(r.Fingers.Line, key+".fingers", "%d is not a %s; it takes at least %d fingers", r.Fingers.V, name, minFingers)
	}
	b.Fingers = uint32(max(r.Fingers.V, 0)) //nolint:gosec // clamped above zero; finger counts are small
	if r.Modifiers != nil {
		set, err := binds.ParseModifiers(r.Modifiers.V)
		if err != nil {
			p.fail(r.Modifiers.Line, key+".modifiers", "%v", err)
		}
		b.Modifiers = set
	}
	switch {
	case directions == nil && r.Direction != nil:
		p.fail(r.Direction.Line, key+".direction", "a %s has no direction", name)
	case directions != nil && r.Direction == nil:
		p.fail(r.Fingers.Line, key+".direction", "is missing")
	case directions != nil:
		if d := choice(p, r.Direction, key+".direction", directions); d != nil {
			b.Direction = *d
		}
	}
	switch {
	case (r.Action == nil) == (r.Drive == nil):
		p.fail(r.Fingers.Line, key, "takes exactly one of action and drive")
	case r.Drive != nil:
		if d := choice(p, r.Drive, key+".drive", gestureDrives); d != nil {
			b.Drive = *d
		}
		if b.Drive == binds.DriveZoom {
			if kind != backend.Pinch || b.Direction != binds.PinchBoth {
				p.fail(r.Drive.Line, key+".drive", "only a pinch can drive zoom, and it goes both ways, so it takes the direction both")
			}
		} else if kind != backend.Swipe {
			p.fail(r.Drive.Line, key+".drive", "only a swipe can drive something")
		} else if b.Drive == binds.DriveWorkspaces && b.Direction != binds.SwipeHorizontal && b.Direction != binds.SwipeVertical {
			p.fail(r.Drive.Line, key+".drive", "workspaces goes both ways, so it takes the direction horizontal or vertical")
		} else if b.Drive == binds.DriveOverview && b.Direction != binds.SwipeVertical && b.Direction != binds.SwipeUp && b.Direction != binds.SwipeDown {
			p.fail(r.Drive.Line, key+".drive", "overview takes the direction up, down or vertical")
		}
	default:
		a, err := wm.ParseAction(r.Action.V)
		if err != nil {
			p.fail(r.Action.Line, key+".action", "%v", err)
		}
		if _, drag := a.(wm.DragAction); drag {
			p.fail(r.Action.Line, key+".action", "%s is for mouse binds only", a)
		}
		b.Action = a
	}
	return b, len(p.errs) == failures
}

func (p *parser) gestures(r rawGestures, input rawInput, workspaces anim.Style) compositor.Gestures {
	g := compositor.DefaultGestures()
	if v := p.between(r.Distance, "gestures.distance", 0, math.MaxFloat64, true); v != nil {
		g.Distance = *v
	}
	if input.Touchpad.NaturalScroll != nil {
		g.Natural = *input.Touchpad.NaturalScroll
	}
	for _, name := range slices.Sorted(maps.Keys(input.Devices)) {
		if v := input.Devices[name].NaturalScroll; v != nil {
			if g.Devices == nil {
				g.Devices = map[string]bool{}
			}
			g.Devices[name] = *v
		}
	}
	g.Binds = nil
	if r.Swipe == nil {
		g.Binds = compositor.DefaultGestureBinds(workspaces)
	}
	kinds := []struct {
		kind       backend.Gesture
		name       string
		minFingers int
		raw        []rawGesture
		directions map[string]binds.GestureDirection
	}{
		{backend.Swipe, "swipe", 3, r.Swipe, swipeDirections},
		{backend.Pinch, "pinch", 2, r.Pinch, pinchDirections},
		{backend.Hold, "hold", 1, r.Hold, nil},
	}
	for _, k := range kinds {
		for i, raw := range k.raw {
			b, ok := p.gesture(k.kind, k.name, k.minFingers, i, raw, k.directions)
			if !ok {
				continue
			}
			if slices.ContainsFunc(g.Binds, b.Overlaps) {
				p.fail(raw.Fingers.Line, fmt.Sprintf("gestures.%s[%d]", k.name, i), "overlaps an earlier %d-finger %s", b.Fingers, k.name)
				continue
			}
			g.Binds = append(g.Binds, b)
		}
	}
	return g
}

func (p *parser) regexp(a *At[string], key string) *regexp.Regexp {
	if a == nil {
		return nil
	}
	re, err := regexp.Compile(a.V)
	if err != nil {
		p.fail(a.Line, key, "%v", err)
	}
	return re
}

func (p *parser) rule(i int, at At[rawRule]) wm.Rule {
	r := at.V
	key := fmt.Sprintf("rules[%d]", i)
	rule := wm.Rule{AppID: p.regexp(r.AppID, key+".app_id"), Title: p.regexp(r.Title, key+".title"), Tag: p.regexp(r.Tag, key+".tag")}
	switch {
	case r.Float && r.Tile:
		p.fail(at.Line, key, "cannot both float and tile")
	case r.Float:
		rule.Mode = wm.RuleFloat
	case r.Tile:
		rule.Mode = wm.RuleTile
	}
	if r.Size != nil {
		size, ok := parsePair(r.Size.V)
		if !ok || size.X <= 0 || size.Y <= 0 {
			p.fail(r.Size.Line, key+".size", "%q is not <width>x<height>", r.Size.V)
		} else {
			rule.Size = size
		}
	}
	switch {
	case r.Tearing == nil:
	case *r.Tearing:
		rule.Tearing = wm.TearingAllow
	default:
		rule.Tearing = wm.TearingDeny
	}
	rule.DimAround, rule.Blur, rule.Mica = r.DimAround, r.Blur, r.Mica
	rule.Look = p.look(r.BlurLook, key+".blur_look")
	rule.OnActivate = choice(p, r.OnActivate, key+".on_activate", activations)
	if v := p.between(r.Opacity, key+".opacity", 0, 1, false); v != nil {
		o := float32(*v)
		rule.Opacity = &o
	}
	if r.AppID == nil && r.Title == nil && r.Tag == nil {
		p.fail(at.Line, key, "matches every window; give app_id, title or tag")
	}
	return rule
}

func (p *parser) decor(raw rawConfig, def compositor.Decor) compositor.Decor {
	return compositor.Decor{
		BorderWidth: p.nonNegative(raw.Borders.Width, "borders.width", def.BorderWidth),
		Rounding:    p.nonNegative(raw.Decoration.Rounding, "decoration.rounding", def.Rounding),
		Opacity: compositor.Opacity{
			Active:     p.opacity(raw.Decoration.ActiveOpacity, "decoration.active_opacity", def.Opacity.Active),
			Inactive:   p.opacity(raw.Decoration.InactiveOpacity, "decoration.inactive_opacity", def.Opacity.Inactive),
			Fullscreen: p.opacity(raw.Decoration.FullscreenOpacity, "decoration.fullscreen_opacity", def.Opacity.Fullscreen),
		},
		Shadow: p.shadow(raw.Decoration.Shadow, def.Shadow),
		Blur: compositor.Blur{
			Enabled:   raw.Decoration.Blur.Enabled != nil && *raw.Decoration.Blur.Enabled,
			Withhold:  def.Blur.Withhold && raw.Decoration.Blur.Enabled == nil && len(raw.Decoration.Blur.Layers) == 0,
			Size:      p.positive(raw.Decoration.Blur.Size, "decoration.blur.size", def.Blur.Size),
			Passes:    p.blurPasses(raw.Decoration.Blur.Passes, def.Blur.Passes),
			XRay:      raw.Decoration.Blur.XRay != nil && *raw.Decoration.Blur.XRay,
			LiveTiled: raw.Decoration.Blur.LiveTiled != nil && *raw.Decoration.Blur.LiveTiled,
			Look:      p.look(raw.Decoration.Blur.rawLook, "decoration.blur").Over(def.Blur.Look),
			Mica: compositor.Mica{
				Enabled: raw.Decoration.Blur.Mica.Enabled != nil && *raw.Decoration.Blur.Mica.Enabled,
				Size:    p.positive(raw.Decoration.Blur.Mica.Size, "decoration.blur.mica.size", 0),
				Passes:  p.micaPasses(raw.Decoration.Blur.Mica.Passes),
				Tint:    p.tint(raw.Decoration.Blur.Mica.Tint, "decoration.blur.mica.tint"),
			},
		},
		Dim: compositor.Dim{
			Inactive: raw.Decoration.DimInactive != nil && *raw.Decoration.DimInactive,
			Strength: p.opacity(raw.Decoration.DimStrength, "decoration.dim_strength", def.Dim.Strength),
		},
		ActiveBorder:   p.border(raw.Borders.Active, "borders.active", def.ActiveBorder),
		InactiveBorder: p.border(raw.Borders.Inactive, "borders.inactive", def.InactiveBorder),
		Tab: compositor.TabStyle{
			Height:      p.positive(raw.Tabs.Height, "tabs.height", def.Tab.Height),
			Padding:     p.nonNegative(raw.Tabs.Padding, "tabs.padding", def.Tab.Padding),
			TextHeight:  p.positive(raw.Tabs.TextHeight, "tabs.text_height", def.Tab.TextHeight),
			TextPadding: p.nonNegative(raw.Tabs.TextPadding, "tabs.text_padding", def.Tab.TextPadding),
			Active:      p.tabColors(raw.Tabs.Active, "tabs.active", def.Tab.Active),
			Inactive:    p.tabColors(raw.Tabs.Inactive, "tabs.inactive", def.Tab.Inactive),
			Urgent:      p.tabColors(raw.Tabs.Urgent, "tabs.urgent", def.Tab.Urgent),
		},
		DragPreview: p.dragPreview(raw.DragPreview, def.DragPreview),
	}
}

func Parse(file string, data []byte) (Config, error) {
	return ParseLayers(compositor.DefaultAccent, Layer{Source: SourceConfig, File: file, Data: data})
}

func ParseLayers(accent render.Color, layers ...Layer) (Config, error) {
	doc, l, err := merged(layers)
	if err != nil {
		return Config{}, err
	}
	file := l.files[0]
	p := &parser{layered: l, palette: map[string]render.Color{}}
	p.unknownKeys(doc, reflect.TypeFor[rawConfig](), "")
	var raw rawConfig
	if doc.Kind != 0 {
		if err := doc.Decode(&raw); err != nil {
			return Config{}, l.decodeError(err)
		}
	}
	maps.Copy(p.palette, mocha)
	p.palette[accentName] = accent
	for _, name := range slices.Sorted(maps.Keys(raw.Palette)) {
		a := raw.Palette[name]
		c, ok := parseHex(a.V)
		if !ok {
			p.fail(a.Line, "palette."+name, "%q is not #rrggbb[aa]", a.V)
		}
		p.palette[name] = c
	}
	look := compositor.DefaultLook(render.GPU, p.palette[accentName])
	cfg := Config{
		layered:         l,
		File:            file,
		Keyboard:        p.keyboard(raw.Input.Keyboard),
		RepeatRate:      p.repeat(raw.Input.Keyboard.RepeatRate, "input.keyboard.repeat_rate", seat.DefaultRepeatRate),
		RepeatDelay:     p.repeat(raw.Input.Keyboard.RepeatDelay, "input.keyboard.repeat_delay", seat.DefaultRepeatDelay),
		Input:           p.input(raw.Input),
		Decor:           p.decor(raw, look.Decor),
		WM:              compositor.DefaultSettings(),
		Xwayland:        raw.Xwayland.Enabled == nil || *raw.Xwayland.Enabled,
		ZeroScaling:     raw.Xwayland.ZeroScaling == nil || *raw.Xwayland.ZeroScaling,
		Tearing:         raw.Tearing,
		ColorManagement: raw.ColorManagement,
		ClientHotkeys:   raw.ClientHotkeys,
		HiddenFrame:     p.hiddenFrame(raw.HiddenFrameRate),
		HoldOpening:     raw.HoldOpening,
		LidSuspend:      raw.LidSuspend == nil || *raw.LidSuspend,
	}
	if raw.Tabs.Font != nil {
		cfg.Font = raw.Tabs.Font.V
	}
	if raw.Tabs.IconTheme != nil {
		cfg.IconTheme = p.iconTheme(*raw.Tabs.IconTheme)
	}
	if raw.GPU != nil {
		cfg.GPU = p.card(*raw.GPU)
	}
	at := &cfg.WM.Autotile
	if raw.Autotile.Enabled != nil {
		at.Enabled = *raw.Autotile.Enabled
	}
	if raw.Autotile.Ephemeral != nil {
		at.Ephemeral = *raw.Autotile.Ephemeral
	}
	at.MinTrigger = p.nonNegative(raw.Autotile.MinTrigger, "autotile.min_trigger", at.MinTrigger)
	at.Divisor = p.positive(raw.Autotile.Divisor, "autotile.divisor", at.Divisor)
	cfg.WM.Gaps = p.gaps(raw.Gaps, cfg.WM.Gaps)
	if v := choice(p, raw.Focus.OnActivate, "focus.on_activate", focusActivations); v != nil {
		cfg.WM.OnActivate = *v
	}
	cfg.Animations = p.animations(raw.Animations, look.Motion)
	cfg.Slower = make(map[render.Speed]compositor.Effects, len(compositor.SlowerSpeeds))
	again := &parser{layered: l, palette: p.palette}
	for _, speed := range compositor.SlowerSpeeds {
		slower := compositor.DefaultLook(speed, p.palette[accentName])
		cfg.Slower[speed] = compositor.Effects{Decor: again.decor(raw, slower.Decor), Animations: again.animations(raw.Animations, slower.Motion), Overview: again.overview(raw.Overview, slower.Overview)}
	}
	cfg.Gestures = p.gestures(raw.Gestures, raw.Input, cfg.Animations.Style(anim.Workspaces))
	cfg.Overview = p.overview(raw.Overview, look.Overview)
	for _, l := range raw.Decoration.Blur.Layers {
		cfg.BlurLayers = append(cfg.BlurLayers, l.V)
	}
	fallback := false
	for i, o := range raw.Outputs {
		rule := p.output(i, o.V)
		if o.V.Match.V != outputFallback {
			cfg.Outputs.Named = append(cfg.Outputs.Named, rule)
			continue
		}
		if fallback {
			p.fail(o.V.Match.Line, fmt.Sprintf("outputs[%d].match", i), "a second %q rule; only one rule can cover the outputs no other rule names", outputFallback)
		}
		fallback, cfg.Outputs.Fallback = true, rule
	}
	for i, b := range raw.Binds {
		cfg.Binds = append(cfg.Binds, p.bind("", i, b, raw.Submaps))
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Submaps)) {
		for i, b := range raw.Submaps[name] {
			cfg.Binds = append(cfg.Binds, p.bind(name, i, b, raw.Submaps))
		}
	}
	for i, r := range raw.Rules {
		cfg.Rules = append(cfg.Rules, p.rule(i, r))
	}
	env := maps.Clone(sessionEnv)
	for _, name := range slices.Sorted(maps.Keys(raw.Env)) {
		value := raw.Env[name]
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value.V, 0) {
			p.fail(value.Line, "env."+name, "is not a valid environment variable")
			continue
		}
		if slices.Contains(reservedEnv, name) {
			p.fail(value.Line, "env."+name, "is set by lys-wm")
			continue
		}
		env[name] = value.V
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		cfg.Env = append(cfg.Env, [2]string{name, env[name]})
	}
	for _, a := range raw.Autostart {
		cfg.Autostart = append(cfg.Autostart, a.V)
	}
	if len(p.errs) != 0 {
		return Config{}, errors.Join(slices.Concat(p.errs, p.warnings)...)
	}
	cfg.Warnings = p.warnings
	return cfg, nil
}

func (c Config) resolveBinds(r binds.Resolver) ([]binds.Bind, error) {
	out := make([]binds.Bind, 0, len(c.Binds))
	src := make([]Bind, 0, len(c.Binds))
	var errs []error
	for _, b := range c.Binds {
		chord, err := binds.ParseChord(b.Chord, r)
		if err != nil {
			file, line := c.where(b.Line)
			errs = append(errs, &Error{File: file, Line: line, Key: "chord", Err: err})
			continue
		}
		out = append(out, binds.Bind{Submap: b.Submap, Chord: chord, Flags: b.Flags, Action: b.Action})
		src = append(src, b)
	}
	for _, d := range binds.Duplicates(out) {
		first, again := src[d[0]], src[d[1]]
		file, line := c.where(again.Line)
		firstFile, firstLine := c.where(first.Line)
		at := fmt.Sprintf("line %d", firstLine)
		if firstFile != file {
			at = fmt.Sprintf("%s:%d", firstFile, firstLine)
		}
		errs = append(errs, &Error{File: file, Line: line, Key: "chord", Err: fmt.Errorf("%q is the same chord as %q at %s", again.Chord, first.Chord, at)})
	}
	return out, errors.Join(errs...)
}

func (c Config) Compositor(keymap *xkb.Keymap, font *gtext.Typeface) (compositor.Config, error) {
	b, err := c.resolveBinds(keymap)
	if err != nil {
		return compositor.Config{}, err
	}
	cfg := compositor.DefaultConfig(keymap, font)
	cfg.Seat.RepeatRate, cfg.Seat.RepeatDelay = c.RepeatRate, c.RepeatDelay
	cfg.WM, cfg.Binds, cfg.Decor, cfg.Rules, cfg.Outputs, cfg.Tearing = c.WM, b, c.Decor, c.Rules, c.Outputs, c.Tearing
	cfg.ColorManagement = c.ColorManagement
	cfg.ClientHotkeys = c.ClientHotkeys
	cfg.HiddenFrame, cfg.HoldOpening = c.HiddenFrame, c.HoldOpening
	cfg.Animations, cfg.BlurLayers, cfg.Slower = c.Animations, c.BlurLayers, c.Slower
	cfg.Gestures = c.Gestures
	if cfg.Gestures.Binds, err = binds.ResolveGestures(c.Gestures.Binds, keymap); err != nil {
		return compositor.Config{}, err
	}
	cfg.Animations, cfg.BlurLayers, cfg.Overview = c.Animations, c.BlurLayers, c.Overview
	cfg.Icons = icontheme.New(c.IconTheme, nil)
	return cfg, nil
}
