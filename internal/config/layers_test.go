package config

import (
	"strings"
	"testing"
)

func layers(config, runtime string) []Layer {
	return []Layer{
		{Source: SourceConfig, File: "config.yaml", Data: []byte(config)},
		{Source: SourceRuntime, File: RuntimeFile, Data: []byte(runtime)},
	}
}

func TestARuntimeOverrideWinsAndWhatItLeavesOutFallsThrough(t *testing.T) {
	cfg, err := ParseLayers(layers("gaps:\n  inner: 3\nborders:\n  width: 5\n", "gaps:\n  inner: 7\n")...)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := Parse("config.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WM.Gaps.Inner != 7 || cfg.Decor.BorderWidth != 5 || cfg.Decor.Rounding != empty.Decor.Rounding {
		t.Fatalf("gaps %d, borders %d, rounding %d; want the override's 7, the file's 5 and the built-in %d", cfg.WM.Gaps.Inner, cfg.Decor.BorderWidth, cfg.Decor.Rounding, empty.Decor.Rounding)
	}
	if cfg.WM.Gaps.Outer != empty.WM.Gaps.Outer {
		t.Fatalf("gaps.outer %v, want the built-in %v beside the overridden gaps.inner", cfg.WM.Gaps.Outer, empty.WM.Gaps.Outer)
	}
}

func TestARuntimeOverrideReplacesAListWhole(t *testing.T) {
	cfg, err := ParseLayers(layers("autostart: [a, b]\n", "autostart: [c]\n")...)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Autostart) != 1 || cfg.Autostart[0] != "c" {
		t.Fatalf("autostart %v, want [c]", cfg.Autostart)
	}
}

func TestARuntimeOverrideAppliesOverTheBuiltInConfig(t *testing.T) {
	cfg, err := ParseLayers(
		Layer{Source: SourceBuiltIn, File: "built-in config", Data: Default},
		Layer{Source: SourceRuntime, File: RuntimeFile, Data: []byte("gaps:\n  inner: 9\n")},
	)
	if err != nil {
		t.Fatal(err)
	}
	builtIn, err := Parse("built-in config", Default)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WM.Gaps.Inner != 9 || len(cfg.Binds) != len(builtIn.Binds) || len(cfg.Binds) == 0 {
		t.Fatalf("gaps %d and %d binds, want 9 and the built-in config's %d", cfg.WM.Gaps.Inner, len(cfg.Binds), len(builtIn.Binds))
	}
}

func TestABadOverrideNamesItsOwnFileLineAndKey(t *testing.T) {
	_, err := ParseLayers(layers("borders:\n  width: 2\n", "\ngaps:\n  inner: -1\n")...)
	if err == nil || !strings.Contains(err.Error(), RuntimeFile+":3: gaps.inner: -1 is not zero or more") {
		t.Fatalf("%v", err)
	}
	_, err = ParseLayers(layers("gaps:\n  inner: -1\n", "borders:\n  width: 2\n")...)
	if err == nil || !strings.Contains(err.Error(), "config.yaml:2: gaps.inner") {
		t.Fatalf("a bad config.yaml key below an override: %v", err)
	}
}

func TestAnOverrideThatDoesNotDecodeNamesItsOwnFileAndLine(t *testing.T) {
	_, err := ParseLayers(layers("gaps:\n  inner: 1\n", "\ntabs:\n  height: tall\n")...)
	if err == nil || !strings.Contains(err.Error(), RuntimeFile+": yaml: unmarshal errors:\n  line 3: cannot unmarshal") {
		t.Fatalf("%v", err)
	}
	_, err = ParseLayers(layers("", "gaps: [\n")...)
	if err == nil || !strings.HasPrefix(err.Error(), RuntimeFile+": ") {
		t.Fatalf("an override that is not YAML: %v", err)
	}
}

func TestAnUnknownKeyInAnOverrideWarnsAtItsLine(t *testing.T) {
	cfg, err := ParseLayers(layers("gaps:\n  inner: 1\n", "gaps:\n  wide: 1\n")...)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.HasPrefix(cfg.Warnings[0].Error(), RuntimeFile+":2: gaps.wide: unknown key") {
		t.Fatalf("warnings %v", cfg.Warnings)
	}
}

func TestEachBindRemembersTheFileItCameFrom(t *testing.T) {
	cfg, err := ParseLayers(layers("binds:\n  - {chord: SUPER + Q, action: kill}\n", "submaps:\n  resize:\n    - {chord: SUPER + Q, action: kill}\n")...)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range cfg.Binds {
		if file, _ := cfg.where(b.Line); (b.Submap == "") != (file == "config.yaml") {
			t.Fatalf("bind %+v comes from %s", b, file)
		}
	}
}

func TestSourcesSayWhichLayerEachKeyComesFrom(t *testing.T) {
	keys, err := Sources(layers("gaps:\n  inner: 3\n  outer: 4\ninput:\n  devices:\n    \"Mouse.v2\": {tap: true}\n", "gaps:\n  inner: 7\n")...)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]KeySource{}
	for _, k := range keys {
		got[strings.Join(k.Path, "/")] = k
	}
	want := map[string]KeySource{
		"gaps/inner":                 {Source: SourceRuntime, File: RuntimeFile, Line: 2, Value: 7},
		"gaps/outer":                 {Source: SourceConfig, File: "config.yaml", Line: 3, Value: 4},
		"input/devices/Mouse.v2/tap": {Source: SourceConfig, File: "config.yaml", Line: 6, Value: true},
	}
	if len(got) != len(want) {
		t.Fatalf("keys %+v", keys)
	}
	for path, w := range want {
		g := got[path]
		if g.Source != w.Source || g.File != w.File || g.Line != w.Line || g.Value != w.Value {
			t.Errorf("%s: %+v, want %+v", path, g, w)
		}
	}
}

func TestOneLayerParsesAsParseDoes(t *testing.T) {
	text := []byte("gaps:\n  inner: 3\ntabs:\n  height: 0\n")
	_, one := Parse("config.yaml", text)
	_, layered := ParseLayers(Layer{Source: SourceConfig, File: "config.yaml", Data: text})
	if one == nil || layered == nil || one.Error() != layered.Error() {
		t.Fatalf("%v, %v", one, layered)
	}
}
