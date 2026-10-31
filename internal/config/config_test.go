package config

import (
	"errors"
	"image"
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
		"animations:\n  events:\n    window: {curve: linear}\n":                 "t.yaml:3: animations.events.window: unknown event (known: global, windows, windows_move, windows_in, windows_out, fade, fade_in, fade_out, workspaces, layers, layers_in, layers_out, fade_switch, fade_dim, fade_shadow)",
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
