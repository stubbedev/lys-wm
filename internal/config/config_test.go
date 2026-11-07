package config

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/stubbedev/lys-wm/internal/xkb"
)

func TestAFallbackOutputRuleIsKeptApart(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("outputs:\n  - match: '*'\n    mode: highres\n    scale: 1.5\n  - match: DP-1\n    scale: 2\n    bitdepth: 10\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Outputs.Named) != 1 || cfg.Outputs.Named[0].Match.Name != "DP-1" || cfg.Outputs.Named[0].Scale != 2*output.Unit || cfg.Outputs.Named[0].Depth != backend.Depth10 {
		t.Errorf("named rules %+v, want DP-1 at scale 2 and 10 bits alone", cfg.Outputs.Named)
	}
	if f := cfg.Outputs.Fallback; f.Mode != (compositor.HighResMode{}) || f.Scale != output.Scale(180) || f.Match.Name != "" || f.Match.Description != "" || f.Depth != backend.Depth8 {
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
	if cfg.Outputs.Named[1].Match.Description != "LG Electronics 27GL850" {
		t.Errorf("second output %+v", cfg.Outputs.Named[1])
	}
	if o := cfg.Outputs.Named[0]; o.Match.Name != "DP-1" || o.Mode != (compositor.ExactMode{Size: image.Pt(2560, 1440), RefreshMHz: 143912}) || o.Scale != output.Scale(150) {
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
	if cfg.Decor.BorderWidth != 2 || cfg.Decor.ActiveBorder != render.SolidBorder(render.Color{R: 0xff, G: 0x88, A: 0xff}) || cfg.Decor.Tab.Height != 18 || cfg.Decor.Tab.Urgent.Text != (render.Color{A: 0xff}) {
		t.Errorf("decor %+v", cfg.Decor)
	}
	if cfg.Decor.Opacity != (compositor.Opacity{Active: 1, Inactive: 0.9, Fullscreen: 1}) {
		t.Errorf("decoration opacity parsed to %+v", cfg.Decor.Opacity)
	}
	if c, err := Parse("t.yaml", []byte("rules:\n  - {app_id: foot, opacity: 0.8}\n")); err != nil || *wm.Decide(c.Rules, wm.Subject{AppID: "foot", Title: ""}).Opacity != 0.8 {
		t.Errorf("a rule's opacity: %+v, %v", c.Rules, err)
	}
	if cfg.Decor.Dim != (compositor.Dim{Inactive: true, Strength: 0.2}) {
		t.Errorf("decoration dim parsed to %+v", cfg.Decor.Dim)
	}
	if c, err := Parse("t.yaml", []byte("rules:\n  - {title: ^Open, dim_around: true}\n")); err != nil || !wm.Decide(c.Rules, wm.Subject{AppID: "", Title: "Open File"}).DimAround {
		t.Errorf("a rule's dim_around: %+v, %v", c.Rules, err)
	}
	if s := cfg.Decor.Shadow; !s.Enabled || s.Range != 12 || s.Power != compositor.DefaultDecor(compositor.DefaultAccent).Shadow.Power || s.Offset != image.Pt(2, 4) || s.Scale != 1 || s.Active != (render.Color{A: 0x80}) || s.Inactive != s.Active {
		t.Errorf("decoration.shadow parsed to %+v", s)
	}
	if c, err := Parse("t.yaml", []byte("borders:\n  active: {colors: [mauve, \"#00ff0080\"], angle: 45}\n")); err != nil || !slices.Equal(c.Decor.ActiveBorder.Colors(), []render.Color{{R: 0xcb, G: 0xa6, B: 0xf7, A: 0xff}, {G: 0xff, A: 0x80}}) {
		t.Errorf("a gradient border: %+v, %v", c.Decor.ActiveBorder, err)
	}
	if c, err := Parse("t.yaml", []byte("decoration:\n  blur:\n    xray: true\n")); err != nil || !c.Decor.Blur.XRay {
		t.Errorf("decoration.blur.xray: %+v, %v", c.Decor.Blur, err)
	}
	if p := wm.DecideLayer(cfg.LayerRules, "waybar"); cfg.Decor.Blur != (compositor.Blur{Enabled: true, Size: compositor.DefaultDecor(compositor.DefaultAccent).Blur.Size, Passes: compositor.DefaultDecor(compositor.DefaultAccent).Blur.Passes, Look: compositor.DefaultDecor(compositor.DefaultAccent).Blur.Look}) || p.Blur == nil || !*p.Blur {
		t.Errorf("decoration.blur parsed to %+v, layer rules %+v", cfg.Decor.Blur, cfg.LayerRules)
	}
	if c, err := Parse("t.yaml", []byte("rules:\n  - {app_id: foot, blur: false}\n")); err != nil || *wm.Decide(c.Rules, wm.Subject{AppID: "foot", Title: ""}).Blur {
		t.Errorf("a rule's blur: %+v, %v", c.Rules, err)
	}
	if cfg.Decor.Rounding != 8 {
		t.Errorf("decoration.rounding parsed to %d", cfg.Decor.Rounding)
	}
	if cfg.Decor.Tab.Padding != compositor.DefaultDecor(compositor.DefaultAccent).Tab.Padding {
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
	if c, err := Parse("t.yaml", []byte("animations:\n  enabled: false\n  events:\n    global: {curve: linear, duration: 100}\n")); err != nil || c.Animations.Curve(anim.Global) != anim.Instant {
		t.Errorf("animations are on with enabled: false: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  enabled: true\n  reduced_motion: true\n")); err != nil || !c.Animations.Reduced() || c.Animations.Curve(anim.WindowsMove) != anim.Instant || c.Animations.Curve(anim.FadeIn) == anim.Instant {
		t.Errorf("reduced_motion: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  enabled: false\n  reduced_motion: true\n")); err != nil || c.Animations.Reduced() || c.Animations.Curve(anim.FadeIn) != anim.Instant {
		t.Errorf("reduced_motion with enabled: false animates: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  reduced_motion: true\n")); err != nil || !c.Animations.Reduced() || c.Animations.Curve(anim.FadeIn) == anim.Instant {
		t.Errorf("reduced_motion on the default animations: %v, %v", c.Animations, err)
	}
	if c, err := Parse("t.yaml", []byte("animations:\n  enabled: true\n")); err != nil || c.Animations.Reduced() {
		t.Errorf("reduced motion is on unasked: %v", err)
	}
	if c, err := Parse("t.yaml", []byte("drag_preview:\n  enabled: false\n  opacity: 0.5\n  blur: true\n  snap_distance: 8\n  snap_windows: true\n  drag_switch: {width: 6, frames: 12}\n")); err != nil || c.Decor.DragPreview != (compositor.DragPreview{Opacity: 0.5, Blur: true, SnapDistance: 8, SnapWindows: true, Switch: compositor.DragSwitch{Width: 6, Frames: 12}}) {
		t.Errorf("drag_preview: %+v, %v", c.Decor.DragPreview, err)
	}
	if c, err := Parse("t.yaml", nil); err != nil || !c.Decor.DragPreview.Enabled {
		t.Errorf("the drag preview is off by default: %+v, %v", c.Decor.DragPreview, err)
	}
	if _, err := Parse("t.yaml", []byte("drag_preview:\n  snap_distance: 0\n")); err == nil || !strings.Contains(err.Error(), "drag_preview.snap_distance") {
		t.Errorf("snap_distance 0: %v", err)
	}
	for _, key := range []string{"width", "frames"} {
		if _, err := Parse("t.yaml", []byte("drag_preview:\n  drag_switch: {"+key+": 0}\n")); err == nil || !strings.Contains(err.Error(), "drag_preview.drag_switch."+key) {
			t.Errorf("drag_switch.%s 0: %v", key, err)
		}
	}
	if c, err := Parse("t.yaml", []byte("floating:\n  step: 25\n")); err != nil || c.WM.FloatStep != 25 {
		t.Errorf("floating.step: %d, %v", c.WM.FloatStep, err)
	}
	if c, err := Parse("t.yaml", nil); err != nil || c.WM.FloatStep != wm.DefaultFloatStep {
		t.Errorf("floating.step defaults to %d, %v", c.WM.FloatStep, err)
	}
	if _, err := Parse("t.yaml", []byte("floating:\n  step: 0\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: floating.step") {
		t.Errorf("floating.step 0: %v", err)
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
	if d := wm.Decide(cfg.Rules, wm.Subject{AppID: "steam", Title: "Friends List"}); d != (wm.Decision{Mode: wm.RuleFloat, Size: wm.Px(400, 900), Tearing: wm.TearingDeny}) {
		t.Errorf("rules decide %+v", d)
	}
	if d := wm.Decide(cfg.Rules, wm.Subject{AppID: "cs2", Title: "Counter-Strike 2"}); d != (wm.Decision{Tearing: wm.TearingAllow}) {
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
	if cfg.Animations != anim.MustResolve(anim.DefaultMotion()) {
		t.Fatalf("animations %+v without config", cfg.Animations)
	}
	if cfg.Decor != compositor.DefaultDecor(compositor.DefaultAccent) || !reflect.DeepEqual(cfg.WM, compositor.DefaultSettings()) || cfg.RepeatRate != 50 || cfg.RepeatDelay != 300 {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestUnknownKeysWarnAndTheRestOfTheFileApplies(t *testing.T) {
	cases := map[string]string{
		"borders:\n  colour: red\n":                                           "t.yaml:2: borders.colour: unknown key (known: active, inactive, width)",
		"submaps:\n  r:\n    - {chord: Q, action: kill, x: 1}\n":              "t.yaml:3: submaps.r[0].x: unknown key",
		"rules:\n  - {app_id: a, floats: true}\n":                             "t.yaml:2: rules[0].floats: unknown key",
		"animations:\n  curves:\n    a: {bezier: [0, 0, 1, 1], tension: 2}\n": "t.yaml:3: animations.curves.a.tension: unknown key",
		"animations:\n  events:\n    border: {curve: linear}\n":               "t.yaml:3: animations.events.border: unknown event",
		"animations:\n  events:\n    window: {curve: linear}\n":               "t.yaml:3: animations.events.window: unknown event (known: global, windows, windows_move, windows_in, windows_out, fade, fade_in, fade_out, workspaces, layers, layers_in, layers_out, fade_switch, fade_dim, fade_shadow, borderangle, windows_minimize, zoom, snap, fade_snap, overview, scratchpad)",
		"borders:\n  active: {colors: [red, blue], angel: 4}\n":               "t.yaml:2: borders.active.angel: unknown key",
		"input:\n  touchpad:\n    tapping: true\n":                            "t.yaml:3: input.touchpad.tapping: unknown key (known: accel_points, accel_profile, accel_speed,",
		"input:\n  devices:\n    mouse:\n      speed: 1\n":                    "t.yaml:4: input.devices.mouse.speed: unknown key",
		"decoration:\n  blur:\n    glint: 1\n":                                "t.yaml:3: decoration.blur.glint: unknown key",
		"from_a_newer_lys_wm: {a: 1}\n":                                       "t.yaml:1: from_a_newer_lys_wm: unknown key (known: ",
	}
	for text, want := range cases {
		cfg, err := Parse("t.yaml", []byte(text+"gpu: card1\n"))
		if err != nil {
			t.Errorf("%q was rejected: %v", text, err)
			continue
		}
		if cfg.GPU != "card1" {
			t.Errorf("%q: the key next to the unknown one did not apply: gpu %q", text, cfg.GPU)
		}
		if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0].Error(), want) {
			t.Errorf("%q warned %v, want %q", text, cfg.Warnings, want)
		}
	}
	if cfg, err := Parse("t.yaml", []byte("gpu: card1\n")); err != nil || len(cfg.Warnings) != 0 {
		t.Errorf("a clean file: %v, warnings %v", err, cfg.Warnings)
	}
}

func TestARejectedFileAlsoNamesItsUnknownKeys(t *testing.T) {
	_, err := Parse("t.yaml", []byte("colour: red\ngpu: renderD128\n"))
	if err == nil || !strings.Contains(err.Error(), "t.yaml:1: colour: unknown key") || !strings.Contains(err.Error(), "t.yaml:2: gpu:") {
		t.Fatalf("got %v", err)
	}
}

func TestErrorsNameFileLineAndKey(t *testing.T) {
	cases := map[string]string{
		"binds:\n  - chord: Q\n    acton: kill\n":                               "t.yaml:3: binds[0].acton: unknown key (known: action, chord, cooldown, description, flags, long_press)",
		"tabs:\n  urgent:\n    fg: red\n":                                       "t.yaml:3: tabs.urgent.fg: unknown key (known: background, text)",
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
		"outputs:\n  - match: DP-1\n    vrr: always\n":                          `t.yaml:3: outputs[0].vrr: "always" is not one of fullscreen, off, on`,
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
		"borders:\n  active: {colors: [red, nope]}\n":                           `t.yaml:2: borders.active.colors[1]: "nope" is neither`,
		"borders:\n  active: {colors: []}\n":                                    "t.yaml:2: borders.active: render: a gradient border takes 2 to 10 colours, not 0",
		"decoration:\n  blur:\n    passes: 5\n":                                 "t.yaml:3: decoration.blur.passes: 5 is not from 1 to 4",
		"decoration:\n  rounding: -1\n":                                         "t.yaml:2: decoration.rounding: -1 is not zero or more",
		"gaps:\n  inner: -2\n":                                                  "t.yaml:2: gaps.inner: -2 is not zero or more",
		"gaps:\n  outer: [1, 2]\n":                                              "t.yaml:2: gaps.outer: [1 2] is not zero or more, or a list of four",
		"gaps:\n  outer: [1, 2, -3, 4]\n":                                       "t.yaml:2: gaps.outer: [1 2 -3 4] is not zero or more",
		"palette:\n  accent: orange\n":                                          `t.yaml:2: palette.accent: "orange" is not #rrggbb[aa]`,
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
	cfg, err := file.Compositor(xkb.Keymaps{Seat: testenv.Keymap(t)}, testenv.Font(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Binds) != len(file.Binds) || cfg.Seat.RepeatRate != seat.DefaultRepeatRate || cfg.Decor != compositor.DefaultDecor(compositor.DefaultAccent) {
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
	cfg, err := file.Compositor(xkb.Keymaps{Seat: testenv.Keymap(t)}, testenv.Font(t))
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

func TestTheBuiltInConfigPressesLysShellsGlobalShortcuts(t *testing.T) {
	file, err := Parse("default.yaml", Default)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]wm.Action{
		"SUPER + Space": wm.GlobalAction{AppID: "dev.stubbe.lys.shell", ID: "launcher"},
		"SUPER + A":     wm.GlobalAction{AppID: "dev.stubbe.lys.shell", ID: "dashboard"},
	}
	for _, b := range file.Binds {
		if a, ok := want[b.Chord]; ok && b.Submap == "" {
			if b.Action != a {
				t.Errorf("%s runs %v, want %v", b.Chord, b.Action, a)
			}
			delete(want, b.Chord)
		}
	}
	if len(want) != 0 {
		t.Errorf("unbound: %v", want)
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
	if r := cfg.Outputs.Named[0]; r.HDR != compositor.HDROn || r.SDRBrightness != 250 || r.HDRPeak != 600 {
		t.Errorf("rule %+v", r)
	}
	if fs, err := Parse("t.yaml", []byte("outputs:\n  - match: DP-1\n    hdr: fullscreen\n")); err != nil || fs.Outputs.Named[0].HDR != compositor.HDRFullscreen {
		t.Errorf("hdr: fullscreen gave %v, %v", fs.Outputs.Named, err)
	}
	plain, err := Parse("t.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.ColorManagement || plain.Outputs.Fallback.HDR != compositor.HDROff {
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
	d := wm.Decide(c.Rules, wm.Subject{AppID: "foot", Title: ""})
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

func TestHotCornersParseWithPerOutputOverrides(t *testing.T) {
	file, err := Parse("corners.yaml", []byte(`
hot_corners:
  top_left: {action: overview toggle}
  bottom_right: {action: kill, modifiers: SUPER}
  pressure: 120
  fullscreen: true
outputs:
  - {match: eDP-1, hot_corners: false}
  - match: DP-1
    hot_corners:
      top_left: false
      top_right: {action: fullscreen}
`))
	if err != nil {
		t.Fatal(err)
	}
	h := file.HotCorners
	want := compositor.HotCornerSet{
		compositor.TopLeft:     {Action: wm.OverviewAction{Mode: wm.OverviewToggle}},
		compositor.BottomRight: {Action: wm.KillAction{}, Modifiers: binds.ModSuper},
	}
	if h.Corners != want || h.Pressure != 120 || !h.Fullscreen {
		t.Fatalf("hot corners %+v", h)
	}
	if got := file.Outputs.Named[0].HotCorners; got == nil || *got != (compositor.HotCornerSet{}) {
		t.Errorf("eDP-1 corners %+v, want all off", got)
	}
	dp := want
	dp[compositor.TopLeft], dp[compositor.TopRight] = compositor.HotCorner{}, compositor.HotCorner{Action: wm.FullscreenAction{}}
	if got := file.Outputs.Named[1].HotCorners; got == nil || *got != dp {
		t.Errorf("DP-1 corners %+v, want %+v", got, dp)
	}
	if file.Outputs.Fallback.HotCorners != nil {
		t.Errorf("an output without hot_corners overrides them: %+v", file.Outputs.Fallback.HotCorners)
	}
	cfg, err := file.Compositor(xkb.Keymaps{Seat: testenv.Keymap(t)}, testenv.Font(t))
	if err != nil {
		t.Fatal(err)
	}
	super, err := binds.ModSuper.Mask(testenv.Keymap(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.HotCorners.Corners[compositor.BottomRight].Mods; got != super || got == 0 {
		t.Errorf("the bottom right corner's modifier mask %#x, want %#x", got, super)
	}
	if got := cfg.Outputs.Named[1].HotCorners[compositor.BottomRight].Mods; got != super {
		t.Errorf("DP-1's inherited corner's modifier mask %#x, want %#x", got, super)
	}
}

func TestHotCornersAreOffByDefault(t *testing.T) {
	file, err := Parse("empty.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if file.HotCorners != compositor.DefaultHotCorners() || file.HotCorners.Corners != (compositor.HotCornerSet{}) {
		t.Fatalf("hot corners %+v", file.HotCorners)
	}
}

func TestBadHotCornersAreReported(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"hot_corners:\n  top_left: {modifiers: SUPER}\n", "hot_corners.top_left.action"},
		{"hot_corners:\n  top_left: {action: nope}\n", "hot_corners.top_left.action"},
		{"hot_corners:\n  top_right: {action: movewindow}\n", "hot_corners.top_right.action"},
		{"hot_corners:\n  bottom_left: {action: kill, modifiers: HYPER}\n", "hot_corners.bottom_left.modifiers"},
		{"hot_corners:\n  top_left: true\n", "a hot corner takes false"},
		{"hot_corners:\n  pressure: 0\n", "hot_corners.pressure"},
		{"outputs:\n  - {match: eDP-1, hot_corners: true}\n", "hot_corners takes false"},
		{"outputs:\n  - {match: eDP-1, hot_corners: {bottom_right: {action: nope}}}\n", "outputs[0].hot_corners.bottom_right.action"},
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

func TestAnEmptyConfigGetsTheDefaultLook(t *testing.T) {
	cfg, err := Parse("empty.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	def := compositor.DefaultDecor(compositor.DefaultAccent)
	if def.Rounding == 0 || !def.Shadow.Enabled || cfg.WM.Gaps == (wm.Gaps{}) {
		t.Fatalf("the default look is plain: %+v, gaps %+v", def, cfg.WM.Gaps)
	}
	if cfg.Decor != def || cfg.WM.Gaps != compositor.DefaultSettings().Gaps {
		t.Errorf("decor %+v, gaps %+v", cfg.Decor, cfg.WM.Gaps)
	}
	if cfg.Decor.Blur.Withhold {
		t.Error("a GPU is refused blur")
	}
	for _, e := range []anim.Event{anim.WindowsMove, anim.WindowsIn, anim.WindowsOut, anim.WindowsMinimize, anim.Workspaces, anim.LayersIn, anim.LayersOut} {
		if cfg.Animations.Curve(e) == anim.Instant {
			t.Errorf("%s does not animate", e)
		}
	}
	if cfg.Animations.Curve(anim.BorderAngle) != anim.Instant {
		t.Error("a border that holds one colour animates its angle")
	}
	soft := cfg.Slower[render.Software]
	if soft.Decor.Rounding != def.Rounding || soft.Decor.Shadow.Enabled || !soft.Decor.Blur.Withhold || soft.Animations.Curve(anim.Workspaces) != anim.Instant {
		t.Errorf("the software renderer's look: %+v", soft)
	}
	if soft.Animations.Curve(anim.WindowsMove) == anim.Instant {
		t.Error("the software renderer's windows do not animate")
	}
	if !cfg.Overview.Blur || soft.Overview.Blur || cfg.Slower[render.CPUDriver].Overview.Blur {
		t.Errorf("overview blur: GPU %v, software %v, CPU driver %v", cfg.Overview.Blur, soft.Overview.Blur, cfg.Slower[render.CPUDriver].Overview.Blur)
	}
	if soft.Overview.Dim != cfg.Overview.Dim {
		t.Errorf("a slower renderer's overview dims %v, want %v", soft.Overview.Dim, cfg.Overview.Dim)
	}
	driver := cfg.Slower[render.CPUDriver]
	if driver.Decor.Rounding != def.Rounding || !driver.Decor.Shadow.Enabled || !driver.Decor.Blur.Withhold || driver.Animations.Curve(anim.Workspaces) == anim.Instant {
		t.Errorf("a CPU driver's look: %+v", driver)
	}
}

func TestExplicitConfigBeatsTheLookOfEveryRenderer(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte(`
decoration:
  rounding: 3
  shadow: {enabled: false}
  blur: {enabled: false}
gaps: {inner: 0, outer: 0}
borders: {width: 2, active: "#112233", inactive: "#445566"}
animations:
  enabled: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WM.Gaps != (wm.Gaps{}) {
		t.Errorf("gaps %+v", cfg.WM.Gaps)
	}
	check := func(name string, d compositor.Decor, a anim.Table) {
		t.Helper()
		if d.Rounding != 3 || d.Shadow.Enabled || d.BorderWidth != 2 || d.Blur.Withhold {
			t.Errorf("%s: %+v", name, d)
		}
		for _, e := range anim.Events() {
			if a.Curve(e) != anim.Instant {
				t.Errorf("%s animates %s with enabled: false", name, e)
			}
		}
	}
	check("the GPU look", cfg.Decor, cfg.Animations)
	for speed, e := range cfg.Slower {
		check(fmt.Sprint("speed ", speed), e.Decor, e.Animations)
	}

	on, err := Parse("t.yaml", []byte("decoration:\n  shadow: {enabled: true}\n  blur: {enabled: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := on.Slower[render.Software].Decor; !s.Shadow.Enabled || !s.Blur.Enabled || s.Blur.Withhold {
		t.Errorf("the software renderer overrides what was asked for: %+v", s)
	}
	blurred, err := Parse("t.yaml", []byte("overview: {blur: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for speed, e := range blurred.Slower {
		if !e.Overview.Blur {
			t.Errorf("speed %d overrides overview.blur: true", speed)
		}
	}
	layers, err := Parse("t.yaml", []byte("layer_rules:\n  - {namespace: ^waybar$, blur: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if layers.Slower[render.CPUDriver].Decor.Blur.Withhold {
		t.Error("a blurred layer rule on a CPU driver is withheld")
	}
}

func TestAConfigsTimingsLayOverTheDefaultMotion(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("animations:\n  events:\n    global: {curve: linear, duration: 100}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Animations.Curve(anim.WindowsMove); c.Duration() != 100*time.Millisecond {
		t.Errorf("a curve on global does not reach windows_move: %v", c.Duration())
	}
	cfg, err = Parse("t.yaml", []byte("animations:\n  events:\n    windows_in: {duration: 90}\n"))
	if err != nil {
		t.Fatalf("a duration over the default spring: %v", err)
	}
	if c := cfg.Animations.Curve(anim.WindowsIn); c.Duration() != 90*time.Millisecond {
		t.Errorf("windows_in with a duration: %v", c.Duration())
	}
	if c := cfg.Animations.Curve(anim.WindowsOut); c != anim.MustResolve(anim.DefaultMotion()).Curve(anim.WindowsOut) {
		t.Error("a duration on windows_in reached windows_out")
	}
	if _, err := Parse("t.yaml", []byte("animations:\n  events:\n    windows_in: {curve: spring, duration: 90}\n")); err == nil {
		t.Error("a duration on a spring the config names is accepted")
	}
}

func TestARuleCanMatchOnTheTagAlone(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("rules:\n  - {tag: ^settings$, float: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Match.Tag == nil || !cfg.Rules[0].Match.Tag.MatchString("settings") || cfg.Rules[0].Mode != wm.RuleFloat {
		t.Fatalf("rules %+v", cfg.Rules)
	}
	if _, err := Parse("t.yaml", []byte("rules:\n  - {tag: \"(\", float: true}\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: rules[0].tag:") {
		t.Fatalf("a bad tag regex: %v", err)
	}
}

func TestClientHotkeysAreOffUnlessTheConfigTurnsThemOn(t *testing.T) {
	for text, want := range map[string]bool{"": false, "client_hotkeys: true\n": true} {
		if got := compositorOf(t, text).ClientHotkeys; got != want {
			t.Errorf("%q: client hotkeys %v, want %v", text, got, want)
		}
	}
}

func compositorOf(t *testing.T, text string) compositor.Config {
	t.Helper()
	file, err := Parse("t.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := file.Compositor(xkb.Keymaps{Seat: testenv.Keymap(t)}, testenv.Font(t))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestHiddenSurfacesGetFrameCallbacksAtTheConfiguredRate(t *testing.T) {
	for text, want := range map[string]time.Duration{"": compositor.DefaultHiddenFrame, "hidden_frame_rate: 5\n": 200 * time.Millisecond, "hidden_frame_rate: 9999999999\n": time.Nanosecond} {
		if got := compositorOf(t, text).HiddenFrame; got != want {
			t.Errorf("%q: hidden surfaces' frame interval %v, want %v", text, got, want)
		}
	}
	if _, err := Parse("t.yaml", []byte("hidden_frame_rate: 0\n")); err == nil || !strings.Contains(err.Error(), "hidden_frame_rate") {
		t.Fatalf("a rate of 0: %v", err)
	}
}

func TestAnOpeningWindowWaitsForItsNeighboursOnlyWhenTheConfigHoldsIt(t *testing.T) {
	for text, want := range map[string]bool{"": false, "hold_opening: true\n": true} {
		if got := compositorOf(t, text).HoldOpening; got != want {
			t.Errorf("%q: hold opening %v, want %v", text, got, want)
		}
	}
}

func TestTiledWindowsBlurLiveOnlyWhenTheConfigAsks(t *testing.T) {
	for text, want := range map[string]bool{"": false, "decoration:\n  blur:\n    enabled: true\n    live_tiled: true\n": true} {
		if got := compositorOf(t, text).Decor.Blur.LiveTiled; got != want {
			t.Errorf("%q: live tiled blur %v, want %v", text, got, want)
		}
	}
}

func TestTheDesktopAccentColoursTheFocusUnlessTheConfigNamesAColour(t *testing.T) {
	desktop := render.Color{R: 0x12, G: 0x34, B: 0x56, A: 0xff}
	parse := func(t *testing.T, text string) Config {
		t.Helper()
		cfg, err := ParseLayers(desktop, Layer{Source: SourceConfig, File: "t.yaml", Data: []byte(text)})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	focus := func(cfg Config) []render.Border {
		out := []render.Border{cfg.Decor.ActiveBorder}
		for _, speed := range compositor.SlowerSpeeds {
			out = append(out, cfg.Slower[speed].Decor.ActiveBorder)
		}
		return out
	}

	cfg := parse(t, "")
	for _, b := range focus(cfg) {
		if b != render.SolidBorder(desktop) {
			t.Errorf("the default focus border is %+v, want the desktop accent", b)
		}
	}
	if got := cfg.Decor.Tab.Active.Background; got != desktop {
		t.Errorf("the default active tab is %+v, want the desktop accent", got)
	}

	gradient, err := render.GradientBorder([]render.Color{desktop, mocha["blue"]}, 45)
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(t, "borders:\n  active: {colors: [accent, blue], angle: 45}\n").Decor.ActiveBorder; got != gradient {
		t.Errorf("a gradient naming accent is %+v, want it from the desktop accent", got)
	}

	explicit := render.Color{R: 0xff, G: 0x88, A: 0xff}
	for name, text := range map[string]string{
		"an explicit border":  "borders:\n  active: \"#ff8800\"\n",
		"an explicit palette": "palette:\n  accent: \"#ff8800\"\n",
	} {
		for _, b := range focus(parse(t, text)) {
			if b != render.SolidBorder(explicit) {
				t.Errorf("%s: the focus border is %+v, want the config's #ff8800", name, b)
			}
		}
	}
	if got := parse(t, "borders:\n  active: \"#ff8800\"\n").Decor.Tab.Active.Background; got != desktop {
		t.Errorf("an explicit border moved the active tab to %+v", got)
	}
}

func TestAnOutputRuleNamesItsAdaptiveSyncPolicy(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("outputs:\n  - match: DP-1\n    vrr: on\n  - match: DP-2\n    vrr: fullscreen\n  - match: DP-3\n    vrr: off\n  - match: DP-4\n"))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []compositor.VRRPolicy{compositor.VRROn, compositor.VRRFullscreen, compositor.VRROff, compositor.VRROff} {
		if got := cfg.Outputs.Named[i].VRR; got != want {
			t.Errorf("%s: vrr %d, want %d", cfg.Outputs.Named[i].Match.Name, got, want)
		}
	}
}

func TestActivationPolicies(t *testing.T) {
	c, err := Parse("t.yaml", []byte("focus:\n  on_activate: urgent\nrules:\n  - {app_id: ^firefox$, on_activate: ignore}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.WM.Focus.OnActivate != wm.ActivateUrgent {
		t.Errorf("focus.on_activate: urgent gave %d", c.WM.Focus.OnActivate)
	}
	if d := wm.Decide(c.Rules, wm.Subject{AppID: "firefox", Title: ""}); d.OnActivate == nil || *d.OnActivate != wm.ActivateIgnore {
		t.Errorf("a rule's on_activate: ignore gave %v", d.OnActivate)
	}
	if d := wm.Decide(c.Rules, wm.Subject{AppID: "foot", Title: ""}); d.OnActivate != nil {
		t.Errorf("a window no rule names got on_activate %d", *d.OnActivate)
	}
	if c, err := Parse("t.yaml", nil); err != nil || c.WM.Focus.OnActivate != wm.ActivateFocus {
		t.Errorf("no focus key: %d, %v; want focus", c.WM.Focus.OnActivate, err)
	}
	if _, err := Parse("t.yaml", []byte("focus:\n  on_activate: ignore\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: focus.on_activate:") {
		t.Errorf("focus.on_activate: ignore was not refused: %v", err)
	}
}

func TestFocusPolicies(t *testing.T) {
	c, err := Parse("t.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (wm.FocusSettings{FollowsMouse: wm.FollowsMouseLoose, WarpPointer: wm.WarpOff, OnClose: wm.CloseFocusesNext}); c.WM.Focus != want {
		t.Errorf("no focus key: %+v, want %+v", c.WM.Focus, want)
	}
	c, err = Parse("t.yaml", []byte("focus:\n  follows_mouse: off\n  raise_on_hover: true\n  warp_pointer: on_keyboard_focus\n  on_close: under_pointer\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (wm.FocusSettings{FollowsMouse: wm.FollowsMouseOff, RaiseOnHover: true, WarpPointer: wm.WarpOnKeyboardFocus, OnClose: wm.CloseFocusesUnderPointer}); c.WM.Focus != want {
		t.Errorf("focus keys: %+v, want %+v", c.WM.Focus, want)
	}
	for _, key := range []string{"follows_mouse", "warp_pointer", "on_close"} {
		if _, err := Parse("t.yaml", []byte("focus:\n  "+key+": sometimes\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: focus."+key+":") {
			t.Errorf("focus.%s: sometimes was not refused: %v", key, err)
		}
	}
}

func TestStickyAndSlowKeysReachTheSeat(t *testing.T) {
	cfg, err := Parse("a11y.yaml", []byte("input:\n  keyboard:\n    sticky_keys: true\n    slow_keys: 300\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.StickyKeys || cfg.SlowKeys != 300*time.Millisecond {
		t.Errorf("sticky %v slow %v", cfg.StickyKeys, cfg.SlowKeys)
	}
	plain, err := Parse("plain.yaml", []byte("input:\n  keyboard:\n    layout: us\n"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.StickyKeys || plain.SlowKeys != 0 {
		t.Errorf("unset keys: sticky %v slow %v", plain.StickyKeys, plain.SlowKeys)
	}
	if _, err := Parse("bad.yaml", []byte("input:\n  keyboard:\n    slow_keys: -5\n")); err == nil {
		t.Error("a negative slow_keys parsed")
	}
}

func TestContentRules(t *testing.T) {
	c, err := Parse("t.yaml", []byte("rules:\n  - {app_id: ^steam_app_, content: game}\n  - {app_id: ^steam_app_, title: Launcher, content: none}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		appID, title string
		want         *wm.ContentType
	}{
		{"steam_app_570", "Dota 2", new(wm.ContentGame)},
		{"steam_app_570", "Launcher", new(wm.ContentNone)},
		{"foot", "", nil},
	} {
		got := wm.Decide(c.Rules, wm.Subject{AppID: tc.appID, Title: tc.title}).Content
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%s %q: content %v, want %v", tc.appID, tc.title, got, tc.want)
		}
	}
	if _, err := Parse("t.yaml", []byte("rules:\n  - {app_id: x, content: movie}\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: rules[0].content:") {
		t.Errorf("content: movie was not refused: %v", err)
	}
}

func TestFullscreenNewWindowPolicy(t *testing.T) {
	for text, want := range map[string]wm.CoverPolicy{"": wm.CoverBehind, "fullscreen:\n  new_window: take_over\n": wm.CoverTakeOver, "fullscreen:\n  new_window: exit\n": wm.CoverExit} {
		c, err := Parse("t.yaml", []byte(text))
		if err != nil || c.WM.NewOverCover != want {
			t.Errorf("%q: %d, %v; want %d", text, c.WM.NewOverCover, err, want)
		}
	}
	if _, err := Parse("t.yaml", []byte("fullscreen:\n  new_window: above\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: fullscreen.new_window:") {
		t.Errorf("fullscreen.new_window: above was not refused: %v", err)
	}
}

func TestPowerSaverFrameRate(t *testing.T) {
	for text, want := range map[string]int{"": compositor.DefaultPowerSaverRate, "power_saver_frame_rate: 30\n": 30, "power_saver_frame_rate: 0\n": 0} {
		c, err := Parse("t.yaml", []byte(text))
		if err != nil || c.PowerSaverRate != want {
			t.Errorf("%q: %d, %v; want %d", text, c.PowerSaverRate, err, want)
		}
	}
	if _, err := Parse("t.yaml", []byte("power_saver_frame_rate: -1\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:1: power_saver_frame_rate:") {
		t.Errorf("a negative rate was not refused: %v", err)
	}
}

func TestScratchpads(t *testing.T) {
	c, err := Parse("t.yaml", []byte("scratchpads:\n  term: {exec: foot --app-id dropterm, size: 60%x50%}\n  notes: {hide_on_workspace_switch: false}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[wm.ScratchpadName]wm.ScratchpadSpec{
		"term":  {Exec: "foot --app-id dropterm", Size: wm.Extent{W: wm.Length{N: 60, Percent: true}, H: wm.Length{N: 50, Percent: true}}},
		"notes": {Stays: true},
	}
	if !reflect.DeepEqual(c.WM.Scratchpads, want) {
		t.Errorf("scratchpads %+v, want %+v", c.WM.Scratchpads, want)
	}
	if _, err := Parse("t.yaml", []byte("scratchpads:\n  term: {size: 60x50}\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: scratchpads.term.size:") {
		t.Errorf("size 60x50 was not refused: %v", err)
	}
}

func TestShowDesktopPeek(t *testing.T) {
	for text, want := range map[string]int{"": wm.DefaultPeek, "showdesktop:\n  peek: 5\n": 5} {
		c, err := Parse("t.yaml", []byte(text))
		if err != nil || c.WM.Peek != want {
			t.Errorf("%q: %d, %v; want %d", text, c.WM.Peek, err, want)
		}
	}
	if _, err := Parse("t.yaml", []byte("showdesktop:\n  peek: 0\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: showdesktop.peek:") {
		t.Errorf("peek 0 was not refused: %v", err)
	}
}

func TestPlacementRules(t *testing.T) {
	c, err := Parse("t.yaml", []byte(`rules:
  - {app_id: ^discord$, workspace: 9 silent, output: "desc:LG Electronics", focus: false}
  - {app_id: ^steam$, title: Friends, float: true, size: 20%x60%, position: 80%x10%, pin: true}
  - {app_id: ^game$, fullscreen: false, suppress: [fullscreen, activate]}
  - {initial_title: ^Open, exclude: {xwayland: true}, group: tab, maximized: true, position: center}
`))
	if err != nil {
		t.Fatal(err)
	}
	d := wm.Decide(c.Rules, wm.Subject{AppID: "discord"})
	if d.Workspace == nil || *d.Workspace != (wm.WorkspacePlacement{ID: 9, Silent: true}) || d.Output == nil || *d.Output != (wm.OutputMatch{Description: "LG Electronics"}) || d.Focus == nil || *d.Focus {
		t.Errorf("discord: %+v", d)
	}
	d = wm.Decide(c.Rules, wm.Subject{AppID: "steam", Title: "Friends List"})
	if d.Size != (wm.Extent{W: wm.Length{N: 20, Percent: true}, H: wm.Length{N: 60, Percent: true}}) || d.Position != (wm.Position{Kind: wm.PositionAt, X: wm.Length{N: 80, Percent: true}, Y: wm.Length{N: 10, Percent: true}}) || d.Pin == nil || !*d.Pin {
		t.Errorf("steam: %+v", d)
	}
	d = wm.Decide(c.Rules, wm.Subject{AppID: "game"})
	if d.Fullscreen == nil || *d.Fullscreen || !d.Suppress.Has(wm.SuppressFullscreen) || !d.Suppress.Has(wm.SuppressActivate) || d.Suppress.Has(wm.SuppressMaximize) {
		t.Errorf("game: %+v", d)
	}
	if d := wm.Decide(c.Rules, wm.Subject{Title: "renamed", InitialTitle: "Open File"}); d.Group == nil || *d.Group != wm.Tabbed || d.Position.Kind != wm.PositionCenter || d.Maximized == nil {
		t.Errorf("by initial title: %+v", d)
	}
	if d := wm.Decide(c.Rules, wm.Subject{InitialTitle: "Open File", XWayland: true}); d.Group != nil {
		t.Errorf("an excluded X window matched: %+v", d)
	}
	for text, want := range map[string]string{
		"rules:\n  - {app_id: a, workspace: nine}\n":      "t.yaml:2: rules[0].workspace:",
		"rules:\n  - {app_id: a, position: middle}\n":     "t.yaml:2: rules[0].position:",
		"rules:\n  - {app_id: a, group: grid}\n":          "t.yaml:2: rules[0].group:",
		"rules:\n  - {app_id: a, suppress: [resize]}\n":   "t.yaml:2: rules[0].suppress[0]:",
		"rules:\n  - {app_id: a, size: 120%x10}\n":        "t.yaml:2: rules[0].size:",
		"rules:\n  - {app_id: a, exclude: {}}\n":          "t.yaml:2: rules[0].exclude:",
		"rules:\n  - {app_id: a, initial_title: \"(\"}\n": "t.yaml:2: rules[0].initial_title:",
		"rules:\n  - {workspace: 2}\n":                    "t.yaml:2: rules[0]:",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
}

func TestBehaviourRules(t *testing.T) {
	c, err := Parse("t.yaml", []byte(`rules:
  - {app_id: ^mpv$, is_fullscreen: true, idle_inhibit: visible, vrr: true, animation: false}
  - {app_id: ^foot$, is_captured: true, border: {active: red, width: 3}, rounding: 0, shadow: false, dim_inactive: false}
  - {app_id: ^gimp$, scroll_factor: 0.5, min_size: 300x200, max_size: 1200x900, keep_aspect: true, animation: {in: slide, out: fade}}
`))
	if err != nil {
		t.Fatal(err)
	}
	d := wm.Decide(c.Rules, wm.Subject{AppID: "mpv", State: wm.WindowState{Fullscreen: true}})
	if d.IdleInhibit != wm.IdleInhibitVisible || d.VRR == nil || !*d.VRR || d.Animation == nil || !d.Animation.Off {
		t.Errorf("mpv fullscreen: %+v", d.Props)
	}
	if d := wm.Decide(c.Rules, wm.Subject{AppID: "mpv"}); d.IdleInhibit != wm.IdleInhibitUnset {
		t.Errorf("mpv windowed matched is_fullscreen: %+v", d.Props)
	}
	d = wm.Decide(c.Rules, wm.Subject{AppID: "foot", State: wm.WindowState{Captured: true}})
	if d.ActiveBorder == nil || d.ActiveBorder.Colors()[0] != (render.Color{R: 0xf3, G: 0x8b, B: 0xa8, A: 0xff}) || d.BorderWidth == nil || *d.BorderWidth != 3 || d.Rounding == nil || *d.Rounding != 0 || d.Shadow == nil || *d.Shadow || d.DimInactive == nil {
		t.Errorf("captured foot: %+v", d.Props)
	}
	d = wm.Decide(c.Rules, wm.Subject{AppID: "gimp"})
	if d.ScrollFactor == nil || *d.ScrollFactor != 0.5 || d.MinSize != image.Pt(300, 200) || d.MaxSize != image.Pt(1200, 900) || d.KeepAspect == nil || d.Animation.In.Kind != anim.Slide || d.Animation.Out.Kind != anim.FadeStyle {
		t.Errorf("gimp: %+v", d.Props)
	}
	for text, want := range map[string]string{
		"rules:\n  - {app_id: a, idle_inhibit: always}\n":  "t.yaml:2: rules[0].idle_inhibit:",
		"rules:\n  - {app_id: a, scroll_factor: 0}\n":      "t.yaml:2: rules[0].scroll_factor:",
		"rules:\n  - {app_id: a, min_size: big}\n":         "t.yaml:2: rules[0].min_size:",
		"rules:\n  - {app_id: a, animation: {in: spin}}\n": "t.yaml:2: rules[0].animation.in:",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
	if _, err := ParseProp("border", "{width: 4}"); err != nil {
		t.Errorf("setprop border: %v", err)
	}
	if _, err := ParseProp("bordr", "4"); err == nil {
		t.Error("setprop took an unknown key")
	}
}

func TestBlockOutFrom(t *testing.T) {
	c, err := Parse("t.yaml", []byte("rules:\n  - {app_id: ^keepassxc$, block_out_from: capture}\nlayer_rules:\n  - {namespace: ^lys-shell-notification-popup$, block_out_from: screencast}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d := wm.Decide(c.Rules, wm.Subject{AppID: "keepassxc"}); d.BlockOutFrom != wm.BlockCapture {
		t.Errorf("window rule: %v", d.BlockOutFrom)
	}
	if p := wm.DecideLayer(c.LayerRules, "lys-shell-notification-popup"); p.BlockOutFrom != wm.BlockScreencast {
		t.Errorf("layer rule: %v", p.BlockOutFrom)
	}
	for text, want := range map[string]string{
		"rules:\n  - {app_id: a, block_out_from: all}\n": "t.yaml:2: rules[0].block_out_from:",
		"layer_rules:\n  - {block_out_from: capture}\n":  "t.yaml:2: layer_rules[0].namespace:",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
}

func TestLayerRules(t *testing.T) {
	c, err := Parse("t.yaml", []byte(`layer_rules:
  - {namespace: ^lys-shell-notification-popup$, animation: {in: slide, out: fade}, block_out_from: screencast}
  - {namespace: ^lys-shell-bar-, blur: true, blur_alpha_threshold: 0.2, shadow: true, rounding: 12, opacity: 0.9, dim_around: true}
  - {namespace: ^lys-shell-wallpaper$, animation: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	p := wm.DecideLayer(c.LayerRules, "lys-shell-notification-popup")
	if p.Animation == nil || p.Animation.In.Kind != anim.Slide || p.Animation.Out.Kind != anim.FadeStyle || p.BlockOutFrom != wm.BlockScreencast {
		t.Errorf("notification: %+v", p)
	}
	p = wm.DecideLayer(c.LayerRules, "lys-shell-bar-DP-1")
	if p.Blur == nil || !*p.Blur || p.BlurAlpha == nil || *p.BlurAlpha != 0.2 || p.Shadow == nil || p.Rounding == nil || *p.Rounding != 12 || p.Opacity == nil || *p.Opacity != 0.9 || !p.DimAround {
		t.Errorf("bar: %+v", p)
	}
	if p := wm.DecideLayer(c.LayerRules, "lys-shell-wallpaper"); p.Animation == nil || !p.Animation.Off {
		t.Errorf("wallpaper: %+v", p)
	}
	c, err = Parse("t.yaml", []byte("decoration:\n  blur: {layers: [waybar]}\n"))
	if err != nil || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0].Error(), "decoration.blur.layers: removed; blur a layer surface with layer_rules") {
		t.Errorf("the removed key: %v, %v", c.Warnings, err)
	}
}

func TestAnOutputsICCProfile(t *testing.T) {
	dir := t.TempDir()
	good, err := os.ReadFile("../harness/testdata/display-p3.icc")
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(os.WriteFile(filepath.Join(dir, "p3.icc"), good, 0o600), os.WriteFile(filepath.Join(dir, "lut.icc"), []byte("not a matrix-shaper profile"), 0o600)); err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(filepath.Join(dir, "config.yaml"), []byte("outputs:\n  - match: DP-1\n    icc: p3.icc\n  - match: DP-2\n    icc: lut.icc\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := cfg.Outputs.Named[0]; r.ICC == nil || r.ICC.Primaries.G.Y < 0.65 || r.ICCPath != filepath.Join(dir, "p3.icc") {
		t.Errorf("a Display P3 profile gave %+v from %q", r.ICC, r.ICCPath)
	}
	if r := cfg.Outputs.Named[1]; r.ICC != nil {
		t.Errorf("a profile ParseICC cannot read gave %+v", r.ICC)
	}
	if !slices.ContainsFunc(cfg.Warnings, func(w error) bool {
		return strings.Contains(w.Error(), "lut.icc") && strings.Contains(w.Error(), "stays sRGB")
	}) {
		t.Errorf("the unreadable profile was not named in a warning: %v", cfg.Warnings)
	}
}

func TestWorkspaceRules(t *testing.T) {
	c, err := Parse("t.yaml", []byte(`workspaces:
  - {id: 1, output: "desc:LG Electronics 27GL850", default: true, persistent: true}
  - {id: 9, name: chat, output: eDP-1, on_created_empty: vesktop}
  - {match: single_tiled, gaps: {inner: 0, outer: 0}, borders: {width: 0}, rounding: 0}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.WorkspaceRules
	if len(r) != 3 || r[0].ID != 1 || r[0].Bind == nil || *r[0].Bind != (wm.OutputMatch{Description: "LG Electronics 27GL850"}) || !r[0].Default || !r[0].Persistent {
		t.Fatalf("rule 0: %+v", r)
	}
	if r[1].Name != "chat" || r[1].OnCreatedEmpty != "vesktop" || *r[1].Bind != (wm.OutputMatch{Name: "eDP-1"}) {
		t.Errorf("rule 1: %+v", r[1])
	}
	if r[2].Select != wm.SelectSingleTiled || r[2].Look.Gaps == nil || *r[2].Look.Gaps != (wm.Gaps{}) || *r[2].Look.BorderWidth != 0 || *r[2].Look.Rounding != 0 {
		t.Errorf("rule 2: %+v", r[2])
	}
	for text, want := range map[string]string{
		"workspaces:\n  - {name: x}\n":                              "t.yaml:2: workspaces[0]:",
		"workspaces:\n  - {match: all}\n":                           "t.yaml:2: workspaces[0].match:",
		"workspaces:\n  - {match: any, name: x}\n":                  "t.yaml:2: workspaces[0]:",
		"workspaces:\n  - {id: 2, name: \"2\"}\n":                   "t.yaml:2: workspaces[0].name:",
		"workspaces:\n  - {id: 2, default: true}\n":                 "t.yaml:2: workspaces[0].default:",
		"workspaces:\n  - {id: 2, name: a}\n  - {id: 3, name: a}\n": "t.yaml:3: workspaces[1].name:",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
}

func TestDeviceSettingsParseIntoTypedValues(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte(`input:
  pointer:
    left_handed: true
    scroll_method: on_button_down
    scroll_button: side
    scroll_button_lock: true
    accel_points: {step: 0.5, points: [0, 1, 2.5]}
  touchpad:
    tap_button_map: lmr
    disabled_on_external_mouse: true
  devices:
    "Trackball":
      scroll_button: 275
    "Wacom Intuos Pro M Pen":
      region: [0.25, 0, 0.5, 0.5]
    "ELAN touchscreen":
      calibration: [0, 1, 0, -1, 0, 1]
`))
	if err != nil {
		t.Fatal(err)
	}
	custom, err := session.AccelCustom(0.5, []float64{0, 1, 2.5})
	if err != nil {
		t.Fatal(err)
	}
	p, tp, d := cfg.Input.Pointer, cfg.Input.Touchpad, cfg.Input.Devices
	if !*p.LeftHanded || *p.ScrollButton != 0x113 || !*p.ScrollButtonLock || p.AccelProfile.String() != custom.String() {
		t.Errorf("pointer %+v", p)
	}
	if *tp.TapButtonMap != session.TapLMR || !*tp.DisabledOnExternalMouse {
		t.Errorf("touchpad %+v", tp)
	}
	if *d["Trackball"].ScrollButton != 275 {
		t.Errorf("a button by code: %v", *d["Trackball"].ScrollButton)
	}
	if a := d["Wacom Intuos Pro M Pen"].Area; a == nil || *a != (session.Area{X1: 0.25, X2: 0.75, Y2: 0.5}) {
		t.Errorf("region %v", a)
	}
	if c := d["ELAN touchscreen"].Calibration; c == nil || *c != (session.Calibration{0, 1, 0, -1, 0, 1}) {
		t.Errorf("calibration %v", c)
	}
	for text, want := range map[string]string{
		"input:\n  pointer:\n    scroll_button: thumb\n":                                             "t.yaml:3: input.pointer.scroll_button:",
		"input:\n  pointer:\n    scroll_button: 0\n":                                                 "t.yaml:3: input.pointer.scroll_button:",
		"input:\n  pointer:\n    accel_profile: flat\n    accel_points: {step: 1, points: [0, 1]}\n": "t.yaml:3: input.pointer.accel_profile: accel_points already selects",
		"input:\n  pointer:\n    accel_points: {step: 1, points: [1]}\n":                             "t.yaml:3: input.pointer.accel_points: 1 points",
		"input:\n  devices:\n    x:\n      region: [0.5, 0, 0.75, 1]\n":                              "t.yaml:4: input.devices.\"x\".region:",
		"input:\n  devices:\n    x:\n      calibration: [1, 0, 0]\n":                                 "t.yaml:4: input.devices.\"x\".calibration: 3 numbers, not 6",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", text, err, want)
		}
	}
}

func TestSwallowTakesTerminalAndExceptionPatterns(t *testing.T) {
	c, err := Parse("t.yaml", []byte("swallow:\n  terminals: ^foot$\n  except_title: wev\n"))
	if err != nil || c.Swallow.Terminals.String() != "^foot$" || c.Swallow.ExceptTitle.String() != "wev" {
		t.Fatalf("swallow parsed to %+v, %v", c.Swallow, err)
	}
	if c, err := Parse("t.yaml", nil); err != nil || c.Swallow.Terminals != nil {
		t.Fatalf("swallowing is on unasked: %+v, %v", c.Swallow, err)
	}
	if _, err := Parse("t.yaml", []byte("swallow:\n  terminals: (foot\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:2: swallow.terminals") {
		t.Fatalf("a bad pattern: %v", err)
	}
}

func TestADeviceKeyboardParsesIntoItsOwnNames(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("input:\n  keyboard:\n    layout: us\n  devices:\n    \"Keychron K2\":\n      keyboard: {layout: dk, options: \"caps:escape\"}\n    \"Mouse\":\n      left_handed: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]xkb.Names{"Keychron K2": {Layout: "dk", Options: "caps:escape"}}; !reflect.DeepEqual(cfg.DeviceKeyboards, want) {
		t.Fatalf("device keyboards %+v", cfg.DeviceKeyboards)
	}
	if _, err := Parse("t.yaml", []byte("input:\n  devices:\n    x:\n      keyboard: {layout: \"us, dk\"}\n")); err == nil || !strings.Contains(err.Error(), `t.yaml:4: input.devices."x".keyboard.layout:`) {
		t.Fatalf("a bad device layout: %v", err)
	}
}

func TestADeviceOutputParsesIntoAnOutputMatch(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("input:\n  devices:\n    \"ELAN touchscreen\":\n      output: eDP-1\n    \"Wacom Pen\":\n      output: \"desc:Dell Inc.\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]wm.OutputMatch{"ELAN touchscreen": {Name: "eDP-1"}, "Wacom Pen": {Description: "Dell Inc."}}
	if !reflect.DeepEqual(cfg.DeviceOutputs, want) {
		t.Fatalf("device outputs %+v", cfg.DeviceOutputs)
	}
	if _, err := Parse("t.yaml", []byte("input:\n  devices:\n    x:\n      output: \"\"\n")); err == nil || !strings.Contains(err.Error(), `t.yaml:4: input.devices."x".output:`) {
		t.Fatalf("an empty output: %v", err)
	}
}

func TestBindFlagsDescriptionsAndModifierTaps(t *testing.T) {
	cfg := compositorOf(t, `binds:
  - {chord: SUPER, action: exec launcher, description: Open the launcher}
  - {chord: SUPER + Q, action: kill, flags: [release, pass, inhibit_proof], long_press: 400, cooldown: 250}
`)
	if len(cfg.Binds) != 2 {
		t.Fatalf("binds %+v", cfg.Binds)
	}
	tap, q := cfg.Binds[0], cfg.Binds[1]
	if tap.Chord.Trigger != (binds.Tap{Modifier: binds.ModSuper}) || !tap.Flags.Release || tap.Description != "Open the launcher" || tap.Text != "SUPER" {
		t.Errorf("a lone SUPER: %+v", tap)
	}
	if want := (binds.Flags{Release: true, Pass: true, InhibitProof: true, LongPress: 400 * time.Millisecond, Cooldown: 250 * time.Millisecond}); q.Flags != want {
		t.Errorf("flags %+v, want %+v", q.Flags, want)
	}
	for text, want := range map[string]string{
		"binds:\n  - {chord: Q, action: kill, flags: [sometimes]}\n":              "t.yaml:2: binds[0].flags:",
		"binds:\n  - {chord: Q, action: kill, long_press: 0}\n":                   "t.yaml:2: binds[0].long_press:",
		"binds:\n  - {chord: Q, action: kill, flags: [release, repeating]}\n":     "release cannot repeat",
		"binds:\n  - {chord: SUPER + mouse_up, action: kill, flags: [release]}\n": "a scroll has no release",
	} {
		file, err := Parse("t.yaml", []byte(text))
		if err == nil {
			_, err = file.Compositor(xkb.Keymaps{Seat: testenv.Keymap(t)}, testenv.Font(t))
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", text, err, want)
		}
	}
}
