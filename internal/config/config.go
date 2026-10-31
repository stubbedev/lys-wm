// Package config reads lys-wm's YAML config into validated types. Every
// error names the file, the line and the key.
package config

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"maps"
	"math"
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
		if name, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); name != "" {
			fields[name] = f.Type
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
				p.fail(key.Line, join(key.Value), "unknown key (known: %s)", strings.Join(slices.Sorted(maps.Keys(fields)), ", "))
				continue
			}
			p.unknownKeys(n.Content[i+1], ft, join(key.Value))
		}
	}
}

type rawConfig struct {
	Outputs    []At[rawOutput]       `yaml:"outputs"`
	Input      rawInput              `yaml:"input"`
	Palette    map[string]At[string] `yaml:"palette"`
	Borders    rawBorders            `yaml:"borders"`
	Gaps       rawGaps               `yaml:"gaps"`
	Decoration rawDecoration         `yaml:"decoration"`
	Animations rawAnimations         `yaml:"animations"`
	Tabs       rawTabs               `yaml:"tabs"`
	Autotile   rawAutotile           `yaml:"autotile"`
	Binds      []rawBind             `yaml:"binds"`
	Submaps    map[string][]rawBind  `yaml:"submaps"`
	Rules      []At[rawRule]         `yaml:"rules"`
	Env        map[string]At[string] `yaml:"env"`
	Autostart  []At[string]          `yaml:"autostart"`
	Xwayland   rawXwayland           `yaml:"xwayland"`
	GPU        *At[string]           `yaml:"gpu"`
	Tearing    bool                  `yaml:"tearing"`
	LidSuspend *bool                 `yaml:"lid_suspend"`
}

type rawXwayland struct {
	Enabled     *bool `yaml:"enabled"`
	ZeroScaling *bool `yaml:"zero_scaling"`
}

type rawOutput struct {
	Match    At[string]   `yaml:"match"`
	Mode     *At[string]  `yaml:"mode"`
	Position *At[string]  `yaml:"position"`
	Scale    *At[float64] `yaml:"scale"`
	BitDepth *At[int]     `yaml:"bitdepth"`
	Disabled bool         `yaml:"disabled"`
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
	Rules       string   `yaml:"rules"`
	Model       string   `yaml:"model"`
	Layout      string   `yaml:"layout"`
	Variant     string   `yaml:"variant"`
	Options     string   `yaml:"options"`
	RepeatRate  *At[int] `yaml:"repeat_rate"`
	RepeatDelay *At[int] `yaml:"repeat_delay"`
}

type rawBorders struct {
	Width    *At[int]    `yaml:"width"`
	Active   *At[string] `yaml:"active"`
	Inactive *At[string] `yaml:"inactive"`
}

type rawAnimations struct {
	Enabled *bool                   `yaml:"enabled"`
	Curves  map[string]At[rawCurve] `yaml:"curves"`
	Events  map[string]At[rawEvent] `yaml:"events"`
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
}

type rawDecoration struct {
	Rounding          *At[int]     `yaml:"rounding"`
	ActiveOpacity     *At[float64] `yaml:"active_opacity"`
	InactiveOpacity   *At[float64] `yaml:"inactive_opacity"`
	FullscreenOpacity *At[float64] `yaml:"fullscreen_opacity"`
	DimInactive       *bool        `yaml:"dim_inactive"`
	DimStrength       *At[float64] `yaml:"dim_strength"`
	Shadow            rawShadow    `yaml:"shadow"`
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

type rawRule struct {
	AppID     *At[string]  `yaml:"app_id"`
	Title     *At[string]  `yaml:"title"`
	Float     bool         `yaml:"float"`
	Tile      bool         `yaml:"tile"`
	Size      *At[string]  `yaml:"size"`
	Tearing   *bool        `yaml:"tearing"`
	Opacity   *At[float64] `yaml:"opacity"`
	DimAround bool         `yaml:"dim_around"`
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
	File        string
	Keyboard    xkb.Names
	RepeatRate  int32
	RepeatDelay int32
	Input       session.InputConfig
	Outputs     compositor.OutputRules
	Decor       compositor.Decor
	Font        string
	IconTheme   string
	WM          wm.Settings
	Binds       []Bind
	Rules       []wm.Rule
	Env         [][2]string
	Autostart   []string
	Xwayland    bool
	ZeroScaling bool
	GPU         string
	Tearing     bool
	LidSuspend  bool
	Animations  anim.Table
}

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
	file    string
	palette map[string]render.Color
	errs    []error
}

func (p *parser) fail(line int, key string, format string, args ...any) {
	p.errs = append(p.errs, &Error{File: p.file, Line: line, Key: key, Err: fmt.Errorf(format, args...)})
}

var cardName = regexp.MustCompile(`^card[0-9]+$`)

func (p *parser) card(a At[string]) string {
	name := strings.TrimPrefix(a.V, "/dev/dri/")
	if !cardName.MatchString(name) {
		p.fail(a.Line, "gpu", "%q is not a DRM card such as card1 or /dev/dri/card1", a.V)
	}
	return name
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

func (p *parser) animations(r rawAnimations) anim.Table {
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
			p.fail(at.Line, key, "unknown event (known: %s)", strings.Join(known, ", "))
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
	table, errs := anim.Resolve(r.Enabled != nil && *r.Enabled, settings)
	for _, e := range anim.Events() {
		if err := errs[e]; err != nil {
			p.fail(lines[e], "animations.events."+e.String(), "%v", err)
		}
	}
	return table
}

func (p *parser) opacity(a *At[float64], key string, fallback float32) float32 {
	if v := p.between(a, key, 0, 1, false); v != nil {
		return float32(*v)
	}
	return fallback
}

func (p *parser) shadow(r rawShadow, def compositor.Shadow) compositor.Shadow {
	s := def
	s.Enabled = r.Enabled != nil && *r.Enabled
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
	s.Inactive = p.optColor(r.ColorInactive, "decoration.shadow.color_inactive", s.Active)
	return s
}

func (p *parser) gaps(r rawGaps) wm.Gaps {
	g := wm.Gaps{Inner: p.nonNegative(r.Inner, "gaps.inner", 0)}
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

const outputFallback = "*"

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
	return rule
}

var (
	clickMethods  = map[string]session.ClickMethod{"none": session.ClickNone, "button_areas": session.ClickButtonAreas, "clickfinger": session.ClickFinger}
	scrollMethods = map[string]session.ScrollMethod{"none": session.ScrollNone, "two_finger": session.ScrollTwoFinger, "edge": session.ScrollEdge, "on_button_down": session.ScrollOnButtonDown}
	accelProfiles = map[string]session.AccelProfile{"flat": session.AccelFlat, "adaptive": session.AccelAdaptive}
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
	rule := wm.Rule{AppID: p.regexp(r.AppID, key+".app_id"), Title: p.regexp(r.Title, key+".title")}
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
	rule.DimAround = r.DimAround
	if v := p.between(r.Opacity, key+".opacity", 0, 1, false); v != nil {
		o := float32(*v)
		rule.Opacity = &o
	}
	if rule.AppID == nil && rule.Title == nil {
		p.fail(at.Line, key, "matches every window; give app_id or title")
	}
	return rule
}

func Parse(file string, data []byte) (Config, error) {
	p := &parser{file: file, palette: map[string]render.Color{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, fmt.Errorf("%s: %w", file, err)
	}
	p.unknownKeys(&doc, reflect.TypeFor[rawConfig](), "")
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%s: %w", file, err)
	}
	maps.Copy(p.palette, mocha)
	for _, name := range slices.Sorted(maps.Keys(raw.Palette)) {
		a := raw.Palette[name]
		c, ok := parseHex(a.V)
		if !ok {
			p.fail(a.Line, "palette."+name, "%q is not #rrggbb[aa]", a.V)
		}
		p.palette[name] = c
	}
	def := compositor.DefaultDecor()
	cfg := Config{
		File: file,
		Keyboard: xkb.Names{
			Rules: raw.Input.Keyboard.Rules, Model: raw.Input.Keyboard.Model, Layout: raw.Input.Keyboard.Layout,
			Variant: raw.Input.Keyboard.Variant, Options: raw.Input.Keyboard.Options,
		},
		RepeatRate:  p.repeat(raw.Input.Keyboard.RepeatRate, "input.keyboard.repeat_rate", seat.DefaultRepeatRate),
		RepeatDelay: p.repeat(raw.Input.Keyboard.RepeatDelay, "input.keyboard.repeat_delay", seat.DefaultRepeatDelay),
		Input:       p.input(raw.Input),
		Decor: compositor.Decor{
			BorderWidth: p.nonNegative(raw.Borders.Width, "borders.width", def.BorderWidth),
			Rounding:    p.nonNegative(raw.Decoration.Rounding, "decoration.rounding", def.Rounding),
			Opacity: compositor.Opacity{
				Active:     p.opacity(raw.Decoration.ActiveOpacity, "decoration.active_opacity", def.Opacity.Active),
				Inactive:   p.opacity(raw.Decoration.InactiveOpacity, "decoration.inactive_opacity", def.Opacity.Inactive),
				Fullscreen: p.opacity(raw.Decoration.FullscreenOpacity, "decoration.fullscreen_opacity", def.Opacity.Fullscreen),
			},
			Shadow: p.shadow(raw.Decoration.Shadow, def.Shadow),
			Dim: compositor.Dim{
				Inactive: raw.Decoration.DimInactive != nil && *raw.Decoration.DimInactive,
				Strength: p.opacity(raw.Decoration.DimStrength, "decoration.dim_strength", def.Dim.Strength),
			},
			ActiveBorder:   p.optColor(raw.Borders.Active, "borders.active", def.ActiveBorder),
			InactiveBorder: p.optColor(raw.Borders.Inactive, "borders.inactive", def.InactiveBorder),
			Tab: compositor.TabStyle{
				Height:      p.positive(raw.Tabs.Height, "tabs.height", def.Tab.Height),
				Padding:     p.nonNegative(raw.Tabs.Padding, "tabs.padding", def.Tab.Padding),
				TextHeight:  p.positive(raw.Tabs.TextHeight, "tabs.text_height", def.Tab.TextHeight),
				TextPadding: p.nonNegative(raw.Tabs.TextPadding, "tabs.text_padding", def.Tab.TextPadding),
				Active:      p.tabColors(raw.Tabs.Active, "tabs.active", def.Tab.Active),
				Inactive:    p.tabColors(raw.Tabs.Inactive, "tabs.inactive", def.Tab.Inactive),
				Urgent:      p.tabColors(raw.Tabs.Urgent, "tabs.urgent", def.Tab.Urgent),
			},
		},
		WM:          compositor.DefaultSettings(),
		Xwayland:    raw.Xwayland.Enabled == nil || *raw.Xwayland.Enabled,
		ZeroScaling: raw.Xwayland.ZeroScaling == nil || *raw.Xwayland.ZeroScaling,
		Tearing:     raw.Tearing,
		LidSuspend:  raw.LidSuspend == nil || *raw.LidSuspend,
	}
	if raw.Tabs.Font != nil {
		cfg.Font = raw.Tabs.Font.V
	}
	if raw.Tabs.IconTheme != nil {
		cfg.IconTheme = raw.Tabs.IconTheme.V
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
	cfg.WM.Gaps = p.gaps(raw.Gaps)
	cfg.Animations = p.animations(raw.Animations)
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
	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) resolveBinds(r binds.Resolver) ([]binds.Bind, error) {
	out := make([]binds.Bind, 0, len(c.Binds))
	src := make([]Bind, 0, len(c.Binds))
	var errs []error
	for _, b := range c.Binds {
		chord, err := binds.ParseChord(b.Chord, r)
		if err != nil {
			errs = append(errs, &Error{File: c.File, Line: b.Line, Key: "chord", Err: err})
			continue
		}
		out = append(out, binds.Bind{Submap: b.Submap, Chord: chord, Flags: b.Flags, Action: b.Action})
		src = append(src, b)
	}
	for _, d := range binds.Duplicates(out) {
		first, again := src[d[0]], src[d[1]]
		errs = append(errs, &Error{File: c.File, Line: again.Line, Key: "chord", Err: fmt.Errorf("%q is the same chord as %q at line %d", again.Chord, first.Chord, first.Line)})
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
	cfg.Animations = c.Animations
	cfg.Icons = icontheme.New(c.IconTheme, nil)
	return cfg, nil
}
