package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/compositor"
	"github.com/stubbedev/lys-wm/internal/render"
	"github.com/stubbedev/lys-wm/internal/wm"
)

func nested(path string, value any) map[string]any {
	keys := strings.Split(path, ".")
	v := value
	for _, key := range slices.Backward(keys) {
		for {
			inner, ok := strings.CutSuffix(key, "[]")
			if !ok {
				break
			}
			key, v = inner, []any{v}
		}
		if key == "*" {
			key = "name"
		}
		v = map[string]any{key: v}
	}
	m, ok := v.(map[string]any)
	if !ok {
		panic("a path has at least one key")
	}
	return m
}

var requires = map[string]map[string]any{
	"binds[].flags[]":          {"binds[].chord": "SUPER + Q", "binds[].action": "kill"},
	"submaps.*[].flags[]":      {"submaps.*[].chord": "SUPER + Q", "submaps.*[].action": "kill"},
	"rules[].blur_look.tint":   {"rules[].app_id": "foot"},
	"rules[].on_activate":      {"rules[].app_id": "foot"},
	"rules[].content":          {"rules[].app_id": "foot"},
	"outputs[].vrr":            {"outputs[].match": "DP-1"},
	"tabs.active.background":   {"tabs.active.text": "text"},
	"tabs.active.text":         {"tabs.active.background": "base"},
	"tabs.inactive.background": {"tabs.inactive.text": "text"},
	"tabs.inactive.text":       {"tabs.inactive.background": "base"},
	"tabs.urgent.background":   {"tabs.urgent.text": "text"},
	"tabs.urgent.text":         {"tabs.urgent.background": "base"},
}

func document(t *testing.T, path string, value any) []byte {
	t.Helper()
	doc := nested(path, value)
	for _, other := range slices.Sorted(maps.Keys(requires[path])) {
		merge(doc, nested(other, requires[path][other]))
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parses(t *testing.T, path string, value any) (Config, error) {
	t.Helper()
	return Parse("schema.yaml", document(t, path, value))
}

func TestTheCommittedSchemaIsWhatTheConfigGenerates(t *testing.T) {
	committed, err := os.ReadFile("../../schema/lys-wm-config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, Schema()) {
		t.Fatal("schema/lys-wm-config.schema.json is stale: run `just schema`")
	}
}

func schemaAt(t *testing.T, root map[string]any, path string) map[string]any {
	t.Helper()
	node := root
	descend := func(key, field string) {
		if branches, ok := node["oneOf"].([]any); ok && field == "properties" {
			for _, b := range branches {
				if m, ok := b.(map[string]any); ok && m["properties"] != nil {
					node = m
				}
			}
		}
		next, ok := node[field].(map[string]any)
		if !ok {
			t.Fatalf("%s: %q has no %s", path, key, field)
		}
		node = next
	}
	for key := range strings.SplitSeq(path, ".") {
		name, lists := key, 0
		for {
			inner, ok := strings.CutSuffix(name, "[]")
			if !ok {
				break
			}
			name, lists = inner, lists+1
		}
		if name == "*" {
			descend(key, "additionalProperties")
		} else {
			descend(key, "properties")
			next, ok := node[name].(map[string]any)
			if !ok {
				t.Fatalf("%s: the schema has no %q", path, name)
			}
			node = next
		}
		for range lists {
			descend(key, "items")
		}
	}
	return node
}

func TestEveryAnnotationNamesAKeyTheSchemaHas(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		node := schemaAt(t, root, path)
		if want := annotations()[path].format; want != "" && node["format"] != want {
			t.Errorf("%s: format %v, want %s", path, node["format"], want)
		}
	}
}

func TestTheStringKeysFromASetTheSystemListsCarryTheirFormat(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"tabs.font":              fontFileFormat,
		"tabs.icon_theme":        iconThemeFormat,
		"input.keyboard.layout":  layoutFormat,
		"input.keyboard.variant": variantFormat,
		"input.keyboard.options": optionsFormat,
		"input.keyboard.model":   modelFormat,
		"input.keyboard.rules":   rulesFormat,
		"gpu":                    drmDeviceFormat,
		"tabs.urgent.text":       colorFormat,
		"rules[].app_id":         regexFormat,
	} {
		if got := schemaAt(t, root, path)["format"]; got != want {
			t.Errorf("%s: format %v, want %s", path, got, want)
		}
	}
}

var formatValues = map[string]struct{ accepted, refused []string }{
	fontFileFormat:  {accepted: []string{"", "/usr/share/fonts/TTF/DejaVuSans.ttf", "fonts/Inter.otf"}},
	iconThemeFormat: {accepted: []string{"", "Papirus-Dark", "hicolor"}, refused: []string{"themes/Papirus", ".."}},
	layoutFormat:    {accepted: []string{"", "us", "us,dk,es"}, refused: []string{"us, dk", "us,,dk", "us,"}},
	variantFormat:   {accepted: []string{"", "dvorak", ",nodeadkeys"}, refused: []string{"intl, dvorak"}},
	optionsFormat:   {accepted: []string{"", "grp:alt_shift_toggle", "grp:alt_shift_toggle,caps:escape"}, refused: []string{"grp:toggle, caps:escape", "caps:escape,"}},
	modelFormat:     {accepted: []string{"", "pc105"}, refused: []string{"pc105,pc104", "pc 105"}},
	rulesFormat:     {accepted: []string{"", "evdev"}, refused: []string{"evdev,base", "ev dev"}},
	drmDeviceFormat: {accepted: []string{"card0", "card1", "/dev/dri/card1"}, refused: []string{"renderD128", "/dev/dri/renderD128", ""}},
	regexFormat:     {accepted: []string{"^foot$", "fire.*"}, refused: []string{"(", "[a-"}},
}

func TestEveryPublishedFormatIsWhatTheParserAccepts(t *testing.T) {
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		format := annotations()[path].format
		if format == "" || format == colorFormat || format == actionFormat {
			continue
		}
		values, ok := formatValues[format]
		if !ok {
			t.Errorf("%s: the format %s has no values to test", path, format)
			continue
		}
		for _, v := range values.accepted {
			if _, err := parses(t, path, v); err != nil {
				t.Errorf("%s: %q is %s but refused: %v", path, v, format, err)
			}
		}
		for _, v := range values.refused {
			if _, err := parses(t, path, v); err == nil {
				t.Errorf("%s: %q is not %s but was accepted", path, v, format)
			}
		}
	}
}

func TestMoreVariantsThanLayoutsAreRefused(t *testing.T) {
	if _, err := Parse("t.yaml", []byte("input:\n  keyboard:\n    layout: us,dk\n    variant: dvorak,,intl\n")); err == nil || !strings.Contains(err.Error(), "t.yaml:4: input.keyboard.variant:") {
		t.Fatalf("three variants for two layouts: %v", err)
	}
	if _, err := Parse("t.yaml", []byte("input:\n  keyboard:\n    layout: us,dk\n    variant: dvorak,\n")); err != nil {
		t.Fatal(err)
	}
}

func TestEveryPublishedEnumIsWhatTheParserAccepts(t *testing.T) {
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		note := annotations()[path]
		if note.enum == nil {
			continue
		}
		for _, v := range note.enum {
			if _, err := parses(t, path, v); err != nil {
				t.Errorf("%s: %q is published but refused: %v", path, v, err)
			}
		}
		if _, err := parses(t, path, "no-such-choice"); err == nil {
			t.Errorf("%s: a value outside the enum was accepted", path)
		}
	}
}

func TestEveryPublishedBoundIsWhatTheParserEnforces(t *testing.T) {
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		note := annotations()[path]
		if note.minimum != nil {
			if _, err := parses(t, path, *note.minimum); err != nil {
				t.Errorf("%s: the minimum %v is refused: %v", path, *note.minimum, err)
			}
			if _, err := parses(t, path, *note.minimum-1); err == nil {
				t.Errorf("%s: below the minimum %v was accepted", path, *note.minimum)
			}
		}
		if note.exclusiveMinimum != nil {
			if _, err := parses(t, path, *note.exclusiveMinimum); err == nil {
				t.Errorf("%s: the exclusive minimum %v was accepted", path, *note.exclusiveMinimum)
			}
		}
		if note.maximum != nil {
			if _, err := parses(t, path, *note.maximum); err != nil {
				t.Errorf("%s: the maximum %v is refused: %v", path, *note.maximum, err)
			}
			if _, err := parses(t, path, *note.maximum+1); err == nil {
				t.Errorf("%s: above the maximum %v was accepted", path, *note.maximum)
			}
		}
	}
}

func TestEveryPublishedItemCountIsWhatTheParserEnforces(t *testing.T) {
	colours := func(n int) []string { return slices.Repeat([]string{"#112233"}, n) }
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		note := annotations()[path]
		if note.minItems != nil {
			if _, err := parses(t, path, colours(*note.minItems)); err != nil {
				t.Errorf("%s: the fewest, %d, are refused: %v", path, *note.minItems, err)
			}
			if _, err := parses(t, path, colours(*note.minItems-1)); err == nil {
				t.Errorf("%s: fewer than %d were accepted", path, *note.minItems)
			}
		}
		if note.maxItems != nil {
			if _, err := parses(t, path, colours(*note.maxItems)); err != nil {
				t.Errorf("%s: the most, %d, are refused: %v", path, *note.maxItems, err)
			}
			if _, err := parses(t, path, colours(*note.maxItems+1)); err == nil {
				t.Errorf("%s: more than %d were accepted", path, *note.maxItems)
			}
		}
	}
}

func TestEveryPublishedColourFieldTakesAColourAndRefusesAnythingElse(t *testing.T) {
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		if annotations()[path].format != colorFormat {
			continue
		}
		for _, v := range []string{"#112233", "#11223344", "mauve"} {
			if _, err := parses(t, path, v); err != nil {
				t.Errorf("%s: %q is refused: %v", path, v, err)
			}
		}
		if _, err := parses(t, path, "not a colour"); err == nil {
			t.Errorf("%s: a non-colour was accepted", path)
		}
	}
}

func comparable(c Config) string {
	c.File = ""
	c.Slower = nil
	return fmt.Sprintf("%+v", c)
}

var writtenTogether = map[string]string{"decoration.shadow.color": "decoration.shadow.color_inactive"}

func withDefaults(paths ...string) map[string]any {
	doc := map[string]any{}
	for _, path := range paths {
		merge(doc, nested(path, annotations()[path].def))
	}
	return doc
}

func merge(into, from map[string]any) {
	for key, v := range from {
		have, isMap := into[key].(map[string]any)
		next, nextIsMap := v.(map[string]any)
		if isMap && nextIsMap {
			merge(have, next)
			continue
		}
		haveList, isList := into[key].([]any)
		nextList, nextIsList := v.([]any)
		if isList && nextIsList && len(haveList) == 1 && len(nextList) == 1 {
			haveItem, ok := haveList[0].(map[string]any)
			nextItem, nextOK := nextList[0].(map[string]any)
			if ok && nextOK {
				merge(haveItem, nextItem)
				continue
			}
		}
		into[key] = v
	}
}

func TestSettingTheShadowColourAloneAlsoMovesTheInactiveOne(t *testing.T) {
	cfg, err := parses(t, "decoration.shadow.color", "#112233")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Decor.Shadow.Inactive != cfg.Decor.Shadow.Active {
		t.Fatalf("inactive %v, active %v", cfg.Decor.Shadow.Inactive, cfg.Decor.Shadow.Active)
	}
}

func TestEveryPublishedDefaultIsWhatTheConfigGetsWithoutTheKey(t *testing.T) {
	for _, accent := range []render.Color{compositor.DefaultAccent, {R: 0x12, G: 0x34, B: 0x56, A: 0xff}} {
		parse := func(data []byte) (Config, error) {
			return ParseLayers(accent, Layer{Source: SourceConfig, File: "schema.yaml", Data: data})
		}
		empty, err := parse(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := comparable(empty)
		for _, path := range slices.Sorted(maps.Keys(annotations())) {
			def := annotations()[path].def
			if def == nil {
				continue
			}
			paths := []string{path}
			if other, ok := writtenTogether[path]; ok {
				paths = append(paths, other)
			}
			data, err := json.Marshal(withDefaults(paths...))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := parse(data)
			if err != nil {
				t.Errorf("%s: the default %v is refused: %v", path, def, err)
				continue
			}
			if got := comparable(cfg); got != want {
				t.Errorf("%s: under the accent %v, writing the published default %v changes the config", path, accent, def)
			}
		}
	}
}

func TestADefaultThatDiffersWouldBeCaught(t *testing.T) {
	empty, err := Parse("schema.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parses(t, "gaps.inner", annotations()["gaps.inner"].def.(int)+1)
	if err != nil {
		t.Fatal(err)
	}
	if comparable(cfg) == comparable(empty) {
		t.Fatal("the comparison does not see a changed key")
	}
}

func TestTheSchemaCoversEveryKeyTheParserKnows(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	props, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatal("the schema's root has no properties")
	}
	if root["additionalProperties"] != false {
		t.Fatal("the schema's root accepts unknown keys, and the parser warns about them")
	}
	got := slices.Sorted(maps.Keys(props))
	want := slices.Sorted(maps.Keys(fieldNames(reflect.TypeFor[rawConfig]())))
	if !slices.Equal(got, want) {
		t.Fatalf("root keys %v, want %v", got, want)
	}
	if !strings.HasSuffix(root["$id"].(string), "schema/lys-wm-config.schema.json") {
		t.Fatalf("$id = %v", root["$id"])
	}
}

func TestTheActionFormatListsEveryActionTheParserKnows(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"binds[].action", "submaps.*[].action", "gestures.swipe[].action", "gestures.pinch[].action", "gestures.hold[].action"} {
		node := schemaAt(t, root, path)
		listed, _ := node["x-lys-wm-actions"].([]any)
		if node["format"] != actionFormat || len(listed) != len(wm.Actions) {
			t.Fatalf("%s: format %v with %d actions, want %s with %d", path, node["format"], len(listed), actionFormat, len(wm.Actions))
		}
	}
	for _, a := range schemaActions() {
		if a.Args != "" {
			continue
		}
		bind := fmt.Sprintf("binds:\n  - {chord: SUPER + Q, action: %s}\n", a.Name)
		if _, err := Parse("t.yaml", []byte(bind)); err != nil && !strings.Contains(err.Error(), "mouse binds only") {
			t.Errorf("%s takes no arguments but a bind with it was refused: %v", a.Name, err)
		}
	}
	if _, err := Parse("t.yaml", []byte("binds:\n  - {chord: SUPER + Q, action: frobnicate}\n")); err == nil {
		t.Error("an action the list does not hold was accepted")
	}
}
