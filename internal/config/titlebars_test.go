package config

import (
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
)

func TestTitlebarsParse(t *testing.T) {
	cfg, err := Parse("t.yaml", []byte("titlebars:\n  floating: false\n  height: 30\n  buttons: [close, minimize]\n  side: left\n  double_click: float\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := compositor.Titlebars{Height: 30, Buttons: [3]compositor.TitlebarButton{compositor.TitlebarClose, compositor.TitlebarMinimize}, Side: compositor.TitlebarLeft, DoubleClick: compositor.DoubleClickFloat}
	if cfg.Decor.Titlebar != want {
		t.Fatalf("titlebars %+v, want %+v", cfg.Decor.Titlebar, want)
	}
	if def, _ := Parse("t.yaml", nil); !def.Decor.Titlebar.Floating || def.Decor.Titlebar.Height != 24 || len(def.Decor.Titlebar.Buttons) != 3 {
		t.Fatalf("default titlebars %+v", def.Decor.Titlebar)
	}
	for text, problem := range map[string]string{
		"titlebars:\n  buttons: [close, close]\n":              `"close" is listed twice`,
		"titlebars:\n  buttons: [close, close, close, help]\n": `"help" is not one of close, maximize, minimize`,
		"titlebars:\n  side: top\n":                            `"top" is not one of left, right`,
		"titlebars:\n  height: 0\n":                            "titlebars.height",
	} {
		if _, err := Parse("t.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("%q: %v, want %q", text, err, problem)
		}
	}
}
