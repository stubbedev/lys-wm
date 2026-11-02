package config

import (
	"errors"
	"image"
	"image/color"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/backend"
	"github.com/stubbedev/lys-wm/internal/binds"
	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/output"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/seat"
	"github.com/stubbedev/lys-wm/internal/session"
	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func TestAFallbackOutputRuleIsKeptApart(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("outputs:\n  - match: '*'\n    mode: highres\n    scale: 1.5\n  - match: DP-1\n    scale: 2\n    bitdepth: 10\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Outputs.Named) != 1 || cfg.Outputs.Named[0].Name != "DP-1" || cfg.Outputs.Named[0].Scale != 2*output.Unit || cfg.Outputs.Named[0].Depth != backend.Depth10 {
		t.Errorf("named rules %+v, want DP-1 at scale 2 and 10 bits alone", cfg.Outputs.Named)
	}
	if f := cfg.Outputs.Fallback; f.Mode != (compositor.HighResMode{}) || f.Scale != output.Scale(180) || f.Name != "" || f.Description != "" || f.Depth != backend.Depth8 {
		t.Errorf("fallback %+v, want highres at scale 1.5 matching nothing by name", f)
	}
}

func TestExampleConfig(t *testing.T) {
	data, err := os.ReadFile("testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse("example.yaml", data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outputs.Named[1].Description != "LG Electronics 27GL850" {
		t.Errorf("second output %+v", cfg.Outputs.Named[1])
	}
	if o := cfg.Outputs.Named[0]; o.Name != "DP-1" || o.Mode != (compositor.ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 143912}) || o.Scale != output.Scale(150) {
		t.Errorf("first output %+v", o)
	}
	if o := cfg.Outputs.Named[1]; o.Mode != (compositor.HighResMode{}) || o.Position != nil {
		t.Errorf("second output %+v", o)
	}
	if !cfg.Outputs.Named[2].Disabled {
		t.Error("HDMI-A-1 is not disabled")
	}
	if cfg.Keyboard.Layout != "us,dk,es" || cfg.RepeatRate != 40 || cfg.RepeatDelay != 250 {
		t.Errorf("keyboard %+v repeat %d %d", cfg.Keyboard, cfg.RepeatRate, cfg.RepeatDelay)
	}
	if cfg.Xwayland {
		t.Error("xwayland.enabled: false left Xwayland on")
	}
	in := cfg.Input
	if *in.Pointer.AccelProfile != session.AccelFlat || *in.Pointer.AccelSpeed != -0.5 || *in.Pointer.ScrollFactor != 1.5 || in.Pointer.Tap != nil {
		t.Errorf("pointer %+v", in.Pointer)
	}
	if !*in.Touchpad.Tap || !*in.Touchpad.NaturalScroll || *in.Touchpad.ClickMethod != session.ClickFinger || *in.Touchpad.ScrollMethod != session.ScrollTwoFinger || in.Touchpad.AccelSpeed != nil {
		t.Errorf("touchpad %+v", in.Touchpad)
	}
	if mx := in.Devices["Logitech MX Master 3"]; len(in.Devices) != 1 || *mx.AccelProfile != session.AccelAdaptive || !*mx.NaturalScroll {
		t.Errorf("devices %+v", in.Devices)
	}
	if cfg.Decor.BorderWidth != 2 || cfg.Decor.ActiveBorder != compositor.SolidBorder(render.Color{R: 0xff, G: 0x88, A: 0xff}) || cfg.Decor.Tab.Height != 18 || cfg.Decor.Tab.Urgent.Text != (render.Color{A: 0xff}) {
		t.Errorf("decor %+v", cfg.Decor)
	}
	if cfg.Decor.Opacity != (compositor.Opacity{Active: 1, Inactive: 0.9, Fullscreen: 1}) {
		t.Errorf("decoration opacity parsed to %+v", cfg.Decor.Opacity)
	}
	if c, err := Parse("t.yaml", []byte("rules:\n  - {app_id: foot, opacity: 0.8}\n")); err != nil || *wm.Decide(c.Rules, "foot", "").Opacity != 0.8 {
		t.Errorf("a rule's opacity: %+v, %v", c.Rules, err)
	}
	if cfg.Decor.Dim != (compositor.Dim{Inactive: true, Strength: 0.2}) {
		t.Errorf("decoration dim parsed to %+v", cfg.Decor.Dim)
	}
	if c, err := Parse("t.yaml", []byte("rules:\n  - {title: ^Open, dim_around: true}\n")); err != nil || !wm.Decide(c.Rules, "", "Open File").DimAround {
		t.Errorf("a rule's dim_around: %+v, %v", c.Rules, err)
	}
	if s := cfg.Decor.Shadow; !s.Enabled || s.Range != 12 || s.Power != 3 || s.Offset != image.Pt(2, 4) || s.Scale != 1 || s.Active != (render.Color{A: 0x80}) || s.Inactive != s.Active {
		t.Errorf("decoration.shadow parsed to %+v", s)
	}
	if c, err := Parse("t.yaml", []byte("borders:\n  active: {colors: [mauve, \"#00ff0080\"], angle: 45}\n")); err != nil || !slices.Equal(c.Decor.ActiveBorder.Colors(), []render.Color{{R: 0xcb, G: 0xa6, B: 0xf7, A: 0xff}, {G: 0xff, A: 0x80}}) {
		t.Errorf("a gradient border: %+v, %v", c.Decor.ActiveBorder, err)
	}
	if c, err := Parse("t.yaml", []byte("decoration:\n  blur:\n    xray: true\n")); err != nil || !c.Decor.Blur.XRay {
		t.Errorf("decoration.blur.xray: %+v, %v", c.Decor.Blur, err)
	}
	if cfg.Decor.Blur != (compositor.Blur{Enabled: true, Size: 8, Passes: 2}) || !slices.Equal(cfg.BlurLayers, []string{"waybar"}) {
		t.Errorf("decoration.blur parsed to %+v, layers %v", cfg.Decor.Blur, cfg.BlurLayers)
	}
	if c, err := Parse("t.yaml", []byte("rules:\n  - {app_id: foot, blur: false}\n")); err != nil || *wm.Decide(c.Rules, "foot", "").Blur {
		t.Errorf("a rule's blur: %+v, %v", c.Rules, err)
	}
	if cfg.Decor.Rounding != 8 {
		t.Errorf("decoration.rounding parsed to %d", cfg.Decor.Rounding)
	}
	if cfg.Decor.Tab.Padding != compositor.DefaultDecor().Tab.Padding {
		t.Error("an unset tab field lost its default")
	}
	if c := cfg.Animations.Curve(anim.Global); c.Duration() != 300*time.Millisecond || c.At(150*time.Millisecond) <= 0.5 {
		t.Errorf("global animates %v over %v", c, c.Duration())
	}
	if wobbly, err := anim.NewSpring(200, 12, 1); err != nil || cfg.Animations.Curve(anim.WindowsMove) != anim.Curve(wobbly) {
		t.Errorf("windows_move animates with %v, want the wobbly spring (%v)", cfg.Animations.Curve(anim.WindowsMove), err)
	}
	if cfg.Animations.Curve(anim.Windows).Duration() != 300*time.Millisecond {
		t.Error("windows does not inherit from global")
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  events:\n    global: {curve: linear, duration: 100}\n")); err != nil || c.Animations.Curve(anim.Global) != anim.Instant {
		t.Errorf("animations are on without enabled: true: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  enabled: true\n  reduced_motion: true\n")); err != nil || !c.Animations.Reduced() || c.Animations.Curve(anim.WindowsMove) != anim.Instant || c.Animations.Curve(anim.FadeIn) == anim.Instant {
		t.Errorf("reduced_motion: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  reduced_motion: true\n")); err != nil || c.Animations != (anim.Table{}) {
		t.Errorf("reduced_motion without enabled animates: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  enabled: true\n")); err != nil || c.Animations.Reduced() {
		t.Errorf("reduced motion is on unasked: %v", err)
	}
	if c, err := Parse("t.yaml", []byte("drag_preview:\n  enabled: false\n  opacity: 0.5\n  blur: true\n  snap_distance: 8\n")); err != nil || c.Decor.DragPreview != (compositor.DragPreview{Opacity: 0.5, Blur: true, SnapDistance: 8}) {
		t.Errorf("drag_preview: %+v, %v", c.Decor.DragPreview, err)
	}
	if c, err := Parse("t.yaml", nil); err != nil || !c.Decor.DragPreview.Enabled {
		t.Errorf("the drag preview is off by default: %+v, %v", c.Decor.DragPreview, err)
	}
	if _, err := Parse("t.yaml", []byte("drag_preview:\n  snap_distance: 0\n")); err == nil || !strings.Contains(err.Error(), "drag_preview.snap_distance") {
		t.Errorf("snap_distance 0: %v", err)
	}
	if cfg.WM.Gaps != (wm.Gaps{Inner: 6, Outer: wm.Insets{Top: 12, Right: 8, Bottom: 12, Left: 8}}) {
		t.Errorf("gaps %+v", cfg.WM.Gaps)
	}
	if c, err := Parse("t.yaml", []byte("gaps:\n  outer: 10\n")); err != nil || c.WM.Gaps.Outer != (wm.Insets{Top: 10, Right: 10, Bottom: 10, Left: 10}) {
		t.Errorf("one outer gap gives %+v, %v", c.WM.Gaps, err)
	}
	if cfg.WM.Autotile.MinTrigger != 500 || !cfg.WM.Autotile.Enabled {
		t.Errorf("autotile %+v", cfg.WM.Autotile)
	}
	if len(cfg.Binds) != 5 || cfg.Binds[3].Submap != "resize" || !cfg.Binds[3].Flags.Repeating || cfg.Binds[3].Action != (wm.ResizeAction{DX: 10}) {
		t.Errorf("binds %+v", cfg.Binds)
	}
	if d := wm.Decide(cfg.Rules, "steam", "Friends List"); d != (wm.Decision{Mode: wm.RuleFloat, Size: image.Pt(400, 900), Tearing: wm.TearingDeny}) {
		t.Errorf("rules decide %+v", d)
	}
	if d := wm.Decide(cfg.Rules, "cs2", "Counter-Strike 2"); d != (wm.Decision{Tearing: wm.TearingAllow}) {
		t.Errorf("rules decide %+v for cs2", d)
	}
	if !cfg.Tearing {
		t.Error("tearing: true left tearing off")
	}
	if !slices.Equal(cfg.Env, [][2]string{{"XDG_CURRENT_DESKTOP", "Hyprland"}, {"XDG_SESSION_TYPE", "wayland"}}) || !slices.Equal(cfg.Autostart, []string{"waybar"}) || cfg.Font != "/usr/share/fonts/DejaVuSans.ttf" {
		t.Errorf("env %v autostart %v font %q", cfg.Env, cfg.Autostart, cfg.Font)
	}
	if cfg.IconTheme != "Papirus" {
		t.Errorf("tabs.icon_theme parsed to %q, want Papirus", cfg.IconTheme)
	}
}

func TestEmptyConfigIsTheDefaults(t *testing.T) {
	cfg, err := Parse("empty.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Env, [][2]string{{"XDG_CURRENT_DESKTOP", "lys-wm"}, {"XDG_SESSION_TYPE", "wayland"}}) {
		t.Fatalf("session env %v", cfg.Env)
	}
	if !cfg.Xwayland || !cfg.ZeroScaling {
		t.Fatal("Xwayland or its zero scaling is off by default")
	}
	if cfg.Tearing {
		t.Fatal("tearing is on by default")
	}
	if cfg.GPU != "" {
		t.Fatalf("a primary GPU %q chosen without config", cfg.GPU)
	}
	for _, text := range []string{"gpu: card1\n", "gpu: /dev/dri/card1\n"} {
		if c, err := Parse("t.yaml", []byte(text)); err != nil || c.GPU != "card1" {
			t.Fatalf("%q gives gpu %q, %v", text, c.GPU, err)
		}
	}
	if cfg.Input.Devices != nil || cfg.Input.Pointer != (session.DeviceConfig{}) || cfg.Input.Touchpad != (session.DeviceConfig{}) {
		t.Fatalf("input %+v", cfg.Input)
	}
	if cfg.Animations != (anim.Table{}) {
		t.Fatalf("animations %+v without config", cfg.Animations)
	}
	if cfg.Decor != compositor.DefaultDecor() || cfg.WM != compositor.DefaultSettings() || cfg.RepeatRate != 50 || cfg.RepeatDelay != 300 {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestErrorsNameFileLineAndKey(t *testing.T) {
	cases := map[string]string{
		"borders:\n  colour: red\n":                                             "t.yaml:2: borders.colour: unknown key (known: active, inactive, width)",
		"binds:\n  - chord: Q\n    acton: kill\n":                               "t.yaml:3: binds[0].acton: unknown key (known: action, chord, flags)",
		"submaps:\n  r:\n    - {chord: Q, action: kill, x: 1}\n":                "t.yaml:3: submaps.r[0].x: unknown key",
		"tabs:\n  urgent:\n    fg: red\n":                                       "t.yaml:3: tabs.urgent.fg: unknown key (known: background, text)",
		"rules:\n  - {app_id: a, floating: true}\n":                             "t.yaml:2: rules[0].floating: unknown key",
		"env:\n  LYS_WM_SOCKET: /x\n":                                           "t.yaml:2: env.LYS_WM_SOCKET: is set by lys-wm",
		"env:\n  A=B: c\n":                                                      "t.yaml:2: env.A=B: is not a valid environment variable",
		"tabs:\n  height: tall\n":                                               "t.yaml: yaml: unmarshal errors:\n  line 2: cannot unmarshal",
		"borders:\n  active: chartreuse\n":                                      `t.yaml:2: borders.active: "chartreuse" is neither`,
		"binds:\n  - chord: SUPER + Q\n    action: teleport\n":                  `t.yaml:3: binds[0].action: bad action: unknown action "teleport"`,
		"binds:\n  - chord: Q\n    action: kill\n    flags: [x]\n":              `t.yaml:4: binds[0].flags: unknown flag "x"`,
		"binds:\n  - chord: SUPER + R\n    action: submap resise\n":             `t.yaml:3: binds[0].action: submap "resise" is not defined under submaps`,
		"rules:\n  - title: '('\n":                                              "t.yaml:2: rules[0].title: error parsing regexp",
		"rules:\n  - app_id: a\n    float: true\n    tile: true\n":              "t.yaml:2: rules[0]: cannot both float and tile",
		"rules:\n  - float: true\n":                                             "t.yaml:2: rules[0]: matches every window",
		"outputs:\n  - match: DP-1\n    mode: big\n":                            `t.yaml:3: outputs[0].mode: "big" is not`,
		"outputs:\n  - match: '*'\n  - match: '*'\n":                            `t.yaml:3: outputs[1].match: a second "*" rule`,
		"outputs:\n  - match: DP-1\n    scale: 20\n":                            "t.yaml:3: outputs[0].scale: output: scale 20 is outside",
		"outputs:\n  - match: DP-1\n    scale: .nan\n":                          "t.yaml:3: outputs[0].scale: output: scale NaN is outside",
		"outputs:\n  - match: DP-1\n    bitdepth: 12\n":                         "t.yaml:3: outputs[0].bitdepth: 12 is not 8 or 10",
		"outputs:\n  - match: DP-1\n    mode: 1x1@nan\n":                        `t.yaml:3: outputs[0].mode: "1x1@nan" is not`,
		"outputs:\n  - match: DP-1\n    mode: 1x1@inf\n":                        `t.yaml:3: outputs[0].mode: "1x1@inf" is not`,
		"outputs:\n  - match: DP-1\n    mode: 1x1@1e12\n":                       `t.yaml:3: outputs[0].mode: "1x1@1e12" is not`,
		"tabs:\n  height: 0\n":                                                  "t.yaml:2: tabs.height: 0 is not a positive number",
		"borders:\n  width: -1\n":                                               "t.yaml:2: borders.width: -1 is not zero or more",
		"animations:\n  curves:\n    a: {bezier: [2, 0, 1, 1]}\n":               "t.yaml:3: animations.curves.a.bezier: anim: bézier x coordinates",
		"animations:\n  curves:\n    a: {bezier: [1, 1]}\n":                     "t.yaml:3: animations.curves.a.bezier: [1 1] is not [x1, y1, x2, y2]",
		"animations:\n  curves:\n    a: {}\n":                                   "t.yaml:3: animations.curves.a: give exactly one of bezier and spring",
		"animations:\n  curves:\n    a: {spring: {stiffness: 1}}\n":             "t.yaml:3: animations.curves.a.spring: anim: spring stiffness 1, damping 0",
		"animations:\n  curves:\n    linear: {bezier: [0, 0, 1, 1]}\n":          "t.yaml:3: animations.curves.linear: is a built-in curve",
		"animations:\n  curves:\n    a: {bezier: [0, 0, 1, 1], tension: 2}\n":   "t.yaml:3: animations.curves.a.tension: unknown key",
		"animations:\n  events:\n    window: {curve: linear}\n":                 "t.yaml:3: animations.events.window: unknown event (known: global, windows, windows_move, windows_in, windows_out, fade, fade_in, fade_out, workspaces, layers, layers_in, layers_out, fade_switch, fade_dim, fade_shadow, border, borderangle, windows_minimize, zoom, snap, fade_snap, overview)",
		"animations:\n  events:\n    windows: {curve: bouncy}\n":                `t.yaml:3: animations.events.windows.curve: "bouncy" is not a curve (known: ease,`,
		"animations:\n  events:\n    windows_in: {style: popin 120%}\n":         "t.yaml:3: animations.events.windows_in.style: anim: popin takes a percentage from 0% to 100%",
		"animations:\n  events:\n    windows_in: {style: wobble}\n":             `t.yaml:3: animations.events.windows_in.style: anim: unknown style "wobble"`,
		"animations:\n  events:\n    fade_in: {style: slide}\n":                 "t.yaml:3: animations.events.fade_in: event fade_in takes no style slide",
		"animations:\n  events:\n    windows: {duration: 0}\n":                  "t.yaml:3: animations.events.windows.duration: 0 is not a positive number of milliseconds",
		"animations:\n  events:\n    windows: {curve: spring, duration: 100}\n": "t.yaml:3: animations.events.windows: event windows: anim: a spring settles in its own time",
		"decoration:\n  active_opacity: 1.5\n":                                  "t.yaml:2: decoration.active_opacity: 1.5 is not from 0 to 1",
		"rules:\n  - {app_id: a, opacity: -1}\n":                                "t.yaml:2: rules[0].opacity: -1 is not from 0 to 1",
		"decoration:\n  dim_strength: 2\n":                                      "t.yaml:2: decoration.dim_strength: 2 is not from 0 to 1",
		"decoration:\n  shadow:\n    render_power: 5\n":                         "t.yaml:3: decoration.shadow.render_power: 5 is not from 1 to 4",
		"decoration:\n  shadow:\n    offset: [1]\n":                             "t.yaml:3: decoration.shadow.offset: [1] is not [x, y]",
		"decoration:\n  shadow:\n    scale: 0\n":                                "t.yaml:3: decoration.shadow.scale: 0 is not above 0 to 1",
		"borders:\n  active: {colors: [red, blue], angel: 4}\n":                 "t.yaml:2: borders.active.angel: unknown key",
		"borders:\n  active: {colors: [red, nope]}\n":                           `t.yaml:2: borders.active.colors[1]: "nope" is neither`,
		"borders:\n  active: {colors: []}\n":                                    "t.yaml:2: borders.active: compositor: a gradient border takes 2 to 10 colours, not 0",
		"decoration:\n  blur:\n    passes: 5\n":                                 "t.yaml:3: decoration.blur.passes: 5 is not from 1 to 4",
		"decoration:\n  rounding: -1\n":                                         "t.yaml:2: decoration.rounding: -1 is not zero or more",
		"gaps:\n  inner: -2\n":                                                  "t.yaml:2: gaps.inner: -2 is not zero or more",
		"gaps:\n  outer: [1, 2]\n":                                              "t.yaml:2: gaps.outer: [1 2] is not zero or more, or a list of four",
		"gaps:\n  outer: [1, 2, -3, 4]\n":                                       "t.yaml:2: gaps.outer: [1 2 -3 4] is not zero or more",
		"palette:\n  accent: orange\n":                                          `t.yaml:2: palette.accent: "orange" is not #rrggbb[aa]`,
		"input:\n  touchpad:\n    tapping: true\n":                              "t.yaml:3: input.touchpad.tapping: unknown key (known: accel_profile, accel_speed,",
		"input:\n  devices:\n    mouse:\n      speed: 1\n":                      `t.yaml:4: input.devices.mouse.speed: unknown key`,
		"input:\n  touchpad:\n    click_method: tap\n":                          `t.yaml:3: input.touchpad.click_method: "tap" is not one of button_areas, clickfinger, none`,
		"input:\n  pointer:\n    accel_speed: 2\n":                              "t.yaml:3: input.pointer.accel_speed: 2 is not from -1 to 1",
		"input:\n  pointer:\n    scroll_factor: 0\n":                            "t.yaml:3: input.pointer.scroll_factor: 0 is not above 0 to 100",
		"input:\n  devices:\n    mouse:\n      accel_profile: x\n":              `t.yaml:4: input.devices."mouse".accel_profile: "x" is not one of adaptive, flat`,
		"gpu: renderD128\n":                                                     `t.yaml:1: gpu: "renderD128" is not a DRM card such as card1 or /dev/dri/card1`,
	}
	for text, want := range cases {
		_, err := Parse("t.yaml", []byte(text))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", text, err, want)
		}
	}
}

type keys struct{}

func (keys) Keycode(name string) (uint32, error) {
	if name == "Q" {
		return 16, nil
	}
	return 0, errors.New("no such key")
}

func (keys) ModMask(string) (uint32, error) { return 64, nil }

func TestChordsResolveWithTheirLine(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("binds:\n  - chord: SUPER + Q\n    action: kill\n  - chord: SUPER + Nope\n    action: kill\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.resolveBinds(keys{})
	var ce *Error
	if !errors.As(err, &ce) || ce.Line != 4 || !errors.Is(err, binds.ErrBadChord) {
		t.Fatalf("an unknown key resolved to %v", err)
	}
}

func TestTwoBindsOnOneChordNameBothLines(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("binds:\n  - chord: SUPER + Q\n    action: kill\n  - chord: SUPER+Q\n    action: reload\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.resolveBinds(keys{})
	var ce *Error
	if !errors.As(err, &ce) || ce.Line != 4 || !strings.Contains(err.Error(), "at line 2") {
		t.Fatalf("a chord bound twice resolved to %v", err)
	}
}

func TestTheBuiltInConfigBuildsACompositorConfig(t *testing.T) {
	file, err := Parse("default.yaml", Default)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := file.Compositor(testenv.Keymap(t), testenv.Font(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Binds) != len(file.Binds) || cfg.Seat.RepeatRate != seat.DefaultRepeatRate || cfg.Decor != compositor.DefaultDecor() {
		t.Fatalf("%d binds of %d, seat %+v", len(cfg.Binds), len(file.Binds), cfg.Seat)
	}
	if _, err := binds.NewMap(cfg.Binds, 0); err != nil {
		t.Fatal(err)
	}
}

func TestTheBuiltInConfigCanLeaveTheSession(t *testing.T) {
	file, err := Parse("default.yaml", Default)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := file.Compositor(testenv.Keymap(t), testenv.Font(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(cfg.Binds, func(b binds.Bind) bool {
		_, exits := b.Action.(wm.ExitAction)
		return exits && b.Submap == ""
	}) {
		t.Fatalf("no top-level bind exits among %d", len(cfg.Binds))
	}
}

func TestOutputShadersResolveAgainstTheConfig(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	cfg, err := Parse("/etc/lys/wm.yaml", []byte("outputs:\n  - match: '*'\n    shader: grade.frag\n  - match: A\n    shader: /abs.frag\n  - match: B\n    shader: ~/mine.frag\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Outputs.Fallback.Shader; got != "/etc/lys/grade.frag" {
		t.Errorf("relative shader %q", got)
	}
	if got := cfg.Outputs.Named[0].Shader; got != "/abs.frag" {
		t.Errorf("absolute shader %q", got)
	}
	if got := cfg.Outputs.Named[1].Shader; got != "/home/me/mine.frag" {
		t.Errorf("home shader %q", got)
	}
	if _, err := Parse("t.yaml", []byte("outputs:\n  - match: A\n    shader: ''\n")); err == nil || !strings.Contains(err.Error(), "outputs[0].shader") {
		t.Errorf("an empty shader: %v", err)
	}
}

func TestHDRKeysReachTheOutputRules(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("color_management: true\noutputs:\n  - match: DP-1\n    hdr: true\n    sdr_brightness: 250\n    hdr_peak: 600\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ColorManagement {
		t.Error("color_management was dropped")
	}
	if r := cfg.Outputs.Named[0]; !r.HDR || r.SDRBrightness != 250 || r.HDRPeak != 600 {
		t.Errorf("rule %+v", r)
	}
	plain, err := Parse("t.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.ColorManagement || plain.Outputs.Fallback.HDR {
		t.Errorf("colour is on by default: %+v", plain)
	}
	for _, bad := range []string{"sdr_brightness: 0", "sdr_brightness: 20000", "hdr_peak: -1"} {
		if _, err := Parse("t.yaml", []byte("outputs:\n  - match: DP-1\n    "+bad+"\n")); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestDecorationBlurLookAndMicaParse(t *testing.T) {
	src := `decoration:
  blur:
    enabled: true
    tint: "#ff000080"
    saturation: 1.5
    brightness: 0.75
    contrast: 1.25
    grain: 0.1
    refraction: {bezel: 12, amount: 6}
    specular: {strength: 0.5, width: 2}
    mica: {enabled: true, size: 20, passes: 3, tint: "#00ff0040"}
rules:
  - {app_id: foot, mica: false, blur_look: {saturation: 2, specular: {width: 3}, refraction: {}}}
`
	c, err := Parse("t.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := wm.Look{
		Tint:            color.RGBA{R: 0x80, A: 0x80},
		SaturationShift: 0.5, BrightnessShift: -0.25, ContrastShift: 0.25, Grain: 0.1,
		Refraction: wm.Refraction{Bezel: 12, Amount: 6},
		Specular:   wm.Specular{Strength: 0.5, Width: 2},
	}
	if c.Decor.Blur.Look != want {
		t.Errorf("look %+v, want %+v", c.Decor.Blur.Look, want)
	}
	if m := c.Decor.Blur.Mica; m != (compositor.Mica{Enabled: true, Size: 20, Passes: 3, Tint: color.RGBA{G: 0x40, A: 0x40}}) {
		t.Errorf("mica %+v", m)
	}
	d := wm.Decide(c.Rules, "foot", "")
	if d.Mica == nil || *d.Mica {
		t.Errorf("a rule's mica: %+v", d.Mica)
	}
	got := d.Look.Over(want)
	want.SaturationShift, want.Specular, want.Refraction = 1, wm.Specular{Strength: 0.6, Width: 3}, wm.Refraction{Bezel: 16, Amount: 8}
	if got != want {
		t.Errorf("a rule's look over the default: %+v, want %+v", got, want)
	}
	for src, msg := range map[string]string{
		"decoration:\n  blur:\n    saturation: 5\n":                       "t.yaml:3: decoration.blur.saturation: 5 is not from 0 to 4",
		"decoration:\n  blur:\n    grain: 2\n":                            "t.yaml:3: decoration.blur.grain: 2 is not from 0 to 1",
		"decoration:\n  blur:\n    mica: {passes: 9}\n":                   "t.yaml:3: decoration.blur.mica.passes: 9 is not from 1 to 4",
		"decoration:\n  blur:\n    glint: 1\n":                            "unknown key",
		"rules:\n  - {app_id: a, blur_look: {specular: {strength: 3}}}\n": "specular.strength: 3 is not from 0 to 1",
	} {
		if _, err := Parse("t.yaml", []byte(src)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", src, err, msg)
		}
	}
}

func gestureConfig(t *testing.T, text string) compositor.Gestures {
	t.Helper()
	cfg, err := Parse("gestures.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Gestures
}

func TestThreeFingersSwitchWorkspacesAndScrubTheOverviewOutOfTheBox(t *testing.T) {
	g := gestureConfig(t, "")
	want := []binds.GestureBind{
		{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeHorizontal, Drive: binds.DriveWorkspaces},
		{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeVertical, Drive: binds.DriveOverview},
	}
	if !slices.Equal(g.Binds, want) || !g.Natural || g.Distance != compositor.DefaultGestureDistance {
		t.Fatalf("defaults %+v", g)
	}
}

func TestSlidevertSwipesWorkspacesVerticallyByDefault(t *testing.T) {
	g := gestureConfig(t, "animations:\n  events:\n    workspaces: {style: slidevert}\n")
	if len(g.Binds) != 1 || g.Binds[0].Direction != binds.SwipeVertical {
		t.Fatalf("binds %+v", g.Binds)
	}
}

func TestAGesturesSection(t *testing.T) {
	g := gestureConfig(t, `
input:
  touchpad: {natural_scroll: false}
  devices:
    "Magic Trackpad": {natural_scroll: true}
    "Other": {tap: true}
gestures:
  distance: 250
  swipe:
    - {fingers: 4, direction: horizontal, drive: workspaces}
    - {fingers: 3, direction: up, action: kill}
  pinch:
    - {fingers: 2, direction: in, action: exec foo}
  hold:
    - {fingers: 4, action: fullscreen}
`)
	if g.Distance != 250 || g.Natural || !maps.Equal(g.Devices, map[string]bool{"Magic Trackpad": true}) {
		t.Fatalf("settings %+v", g)
	}
	want := []binds.GestureBind{
		{Kind: backend.Swipe, Fingers: 4, Direction: binds.SwipeHorizontal, Drive: binds.DriveWorkspaces},
		{Kind: backend.Swipe, Fingers: 3, Direction: binds.SwipeUp, Action: wm.KillAction{}},
		{Kind: backend.Pinch, Fingers: 2, Direction: binds.PinchIn, Action: wm.ExecAction{Command: "foo"}},
		{Kind: backend.Hold, Fingers: 4, Action: wm.FullscreenAction{}},
	}
	if !slices.Equal(g.Binds, want) {
		t.Fatalf("binds %+v\nwant %+v", g.Binds, want)
	}
}

func TestAPinchDrivesZoomWithItsModifiers(t *testing.T) {
	g := gestureConfig(t, "gestures:\n  swipe: []\n  pinch:\n    - {fingers: 2, modifiers: SUPER + CTRL, direction: both, drive: zoom}\n    - {fingers: 2, direction: in, action: kill}\n")
	want := []binds.GestureBind{
		{Kind: backend.Pinch, Fingers: 2, Direction: binds.PinchBoth, Drive: binds.DriveZoom, Modifiers: binds.ModSuper | binds.ModControl},
		{Kind: backend.Pinch, Fingers: 2, Direction: binds.PinchIn, Action: wm.KillAction{}},
	}
	if !slices.Equal(g.Binds, want) {
		t.Fatalf("binds %+v\nwant %+v", g.Binds, want)
	}
	for text, key := range map[string]string{
		"gestures:\n  swipe:\n    - {fingers: 3, direction: horizontal, drive: zoom}\n":             "gestures.swipe[0].drive",
		"gestures:\n  pinch:\n    - {fingers: 2, direction: in, drive: zoom}\n":                     "gestures.pinch[0].drive",
		"gestures:\n  pinch:\n    - {fingers: 2, modifiers: HYPER, direction: both, drive: zoom}\n": "gestures.pinch[0].modifiers",
		"gestures:\n  pinch:\n    - {fingers: 2, direction: both, drive: workspaces}\n":             "gestures.pinch[0].drive",
	} {
		if _, err := Parse("gestures.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%q: %v, want %s named", text, err, key)
		}
	}
}

func TestAnEmptySwipeListTurnsTheDefaultOff(t *testing.T) {
	if g := gestureConfig(t, "gestures:\n  swipe: []\n"); len(g.Binds) != 0 {
		t.Fatalf("binds %+v", g.Binds)
	}
}

func TestBadGesturesAreReported(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"gestures:\n  swipe:\n    - {fingers: 2, direction: up, action: kill}\n", "gestures.swipe[0].fingers"},
		{"gestures:\n  pinch:\n    - {fingers: 1, direction: in, action: kill}\n", "gestures.pinch[0].fingers"},
		{"gestures:\n  swipe:\n    - {fingers: 3, action: kill}\n", "gestures.swipe[0].direction"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: sideways, action: kill}\n", "gestures.swipe[0].direction"},
		{"gestures:\n  pinch:\n    - {fingers: 3, direction: up, action: kill}\n", "gestures.pinch[0].direction"},
		{"gestures:\n  hold:\n    - {fingers: 3, direction: up, action: kill}\n", "gestures.hold[0].direction"},
		{"gestures:\n  hold:\n    - {fingers: 3}\n", "gestures.hold[0]"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: up, action: kill, drive: workspaces}\n", "gestures.swipe[0]"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: up, drive: workspaces}\n", "gestures.swipe[0].drive"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: left, drive: nope}\n", "gestures.swipe[0].drive"},
		{"gestures:\n  pinch:\n    - {fingers: 3, direction: in, drive: workspaces}\n", "gestures.pinch[0].drive"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: up, action: nope}\n", "gestures.swipe[0].action"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: up, action: movewindow}\n", "gestures.swipe[0].action"},
		{"gestures:\n  swipe:\n    - {fingers: 3, direction: horizontal, drive: workspaces}\n    - {fingers: 3, direction: left, action: kill}\n", "gestures.swipe[1]"},
		{"gestures:\n  distance: 0\n", "gestures.distance"},
	} {
		_, err := Parse("bad.yaml", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one naming %s", tc.text, err, tc.want)
		}
	}
}

func TestTheOverviewKeysParseAndCheck(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("overview:\n  blur: false\n  dim: 0.5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Overview.Blur || cfg.Overview.Dim != 0.5 {
		t.Errorf("overview %+v", cfg.Overview)
	}
	if _, err := Parse("t.yaml", []byte("overview:\n  dim: 2\n")); err == nil || !strings.Contains(err.Error(), "overview.dim") {
		t.Errorf("a dim of 2: %v", err)
	}
	if _, err := Parse("t.yaml", []byte("gestures:\n  swipe:\n    - {fingers: 3, direction: left, drive: overview}\n")); err == nil || !strings.Contains(err.Error(), "overview takes") {
		t.Errorf("a sideways overview: %v", err)
	}
}
