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
)

func nested(path string, value any) map[string]any {
	keys := strings.Split(path, ".")
	v := value
	for _, key := range slices.Backward(keys) {
		v = map[string]any{key: v}
	}
	m, ok := v.(map[string]any)
	if !ok {
		panic("a path has at least one key")
	}
	return m
}

func document(t *testing.T, path string, value any) []byte {
	t.Helper()
	data, err := json.Marshal(nested(path, value))
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

func TestEveryAnnotationNamesAKeyTheSchemaHas(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	for _, path := range slices.Sorted(maps.Keys(annotations())) {
		node := root
		for key := range strings.SplitSeq(path, ".") {
			props, ok := node["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s: %q has no properties", path, key)
			}
			if node, ok = props[key].(map[string]any); !ok {
				t.Fatalf("%s: the schema has no %q", path, key)
			}
		}
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
	empty, err := Parse("schema.yaml", nil)
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
		cfg, err := Parse("schema.yaml", data)
		if err != nil {
			t.Errorf("%s: the default %v is refused: %v", path, def, err)
			continue
		}
		if got := comparable(cfg); got != want {
			t.Errorf("%s: writing the published default %v changes the config", path, def)
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
		t.Fatal("the schema's root accepts unknown keys, and the parser does not")
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
