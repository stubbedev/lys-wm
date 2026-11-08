package config

import (
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
)

func TestTitlebarsParse(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("titlebars:\n  floating: false\n  height: 30\n  buttons: [close, minimize]\n  side: left\n  double_click: float\n"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := Parse("t.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := def.Decor.Titlebar
	want.Floating, want.Height, want.Side, want.DoubleClick = false, 30, compositor.TitlebarLeft, compositor.DoubleClickFloat
	want.Buttons = [3]compositor.TitlebarButton{compositor.TitlebarClose, compositor.TitlebarMinimize}
	if cfg.Decor.Titlebar != want {
		t.Fatalf("titlebars %+v, want %+v", cfg.Decor.Titlebar, want)
	}
	wantButtons := [3]compositor.TitlebarButton{compositor.TitlebarClose, compositor.TitlebarMinimize, compositor.TitlebarMaximize}
	if got := def.Decor.Titlebar; !got.Floating || got.Height != 24 || got.Buttons != wantButtons || got.Side != compositor.TitlebarLeft {
		t.Fatalf("default titlebars %+v", got)
	}
	if got := def.Decor.Titlebar.Active.Background; got == def.Decor.Tab.Active.Background {
		t.Fatalf("the default titlebar has the highlight colour %v", got)
	}
	styled, err := Parse("t.yaml", []byte("titlebars:\n  side: right\n  active: {background: \"#102030\", text: crust}\n  lights: {close: \"#405060\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := styled.Decor.Titlebar
	if got.Side != compositor.TitlebarRight || got.Active.Background != (render.Color{R: 0x10, G: 0x20, B: 0x30, A: 0xff}) || got.Lights.Close != (render.Color{R: 0x40, G: 0x50, B: 0x60, A: 0xff}) || got.Lights.Minimize != def.Decor.Titlebar.Lights.Minimize {
		t.Fatalf("styled titlebars %+v", got)
	}
	for text, problem := range map[string]string{
		"titlebars:\n  buttons: [close, close]\n":              `"close" is listed twice`,
		"titlebars:\n  buttons: [close, close, close, help]\n": `"help" is not one of close, maximize, minimize`,
		"titlebars:\n  side: top\n":                            `"top" is not one of left, right`,
		"titlebars:\n  height: 0\n":                            "titlebars.height",
		"titlebars:\n  lights: {close: nope}\n":                "titlebars.lights.close",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q: %v, want %q", text, err, problem)
		}
	}
}
