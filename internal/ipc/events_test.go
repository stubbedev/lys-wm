package ipc

import (
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-wm/internal/wm"
)

func marshalled(t *testing.T, e Event) string {
	t.Helper()
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	name, err := json.Marshal(e.Name())
	if err != nil {
		t.Fatal(err)
	}
	return `{"event":` + string(name) + "," + string(body[1:]) + "\n"
}

func TestHandEncodedEventsMatchEncodingJSON(t *testing.T) {
	names := []string{"", "bar", `quo"te\back`, "<html>&amp;", "tab\tnew\nline\r\b\f\x00\x1f\x7f", "ünïcødé ✓", "  ", "bad\xffutf8\xc3"}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		b := make([]byte, rng.IntN(12))
		for i := range b {
			b[i] = byte(rng.IntN(256))
		}
		names = append(names, string(b))
	}
	for _, n := range names {
		for _, e := range []Event{
			LayerEvent{Change: LayerOpened, ID: 7, Namespace: n, Output: n, Layer: LayerTop},
			ModeEvent{Window: 3, Floating: true, Maximized: len(n)%2 == 0},
		} {
			got, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			if want := marshalled(t, e); string(got) != want {
				t.Fatalf("%T with %q encodes as %s, encoding/json as %s", e, n, got, want)
			}
		}
	}
}

func TestEncodingAModeOrALayerEventAllocatesNothing(t *testing.T) {
	buf := make([]byte, 0, 512)
	var mode Event = ModeEvent{Window: 9, Floating: true, Fullscreen: true}
	var layer Event = LayerEvent{Change: LayerClosed, ID: 2, Namespace: "waybar", Output: "DP-1", Layer: LayerTop}
	for _, e := range []Event{mode, layer} {
		if n := testing.AllocsPerRun(100, func() {
			var err error
			if buf, err = AppendEvent(buf[:0], e); err != nil {
				t.Fatal(err)
			}
		}); n != 0 {
			t.Errorf("encoding a %s event allocates %v times", e.Name(), n)
		}
	}
}

func TestLayersGroupsAndFloatingAreEvents(t *testing.T) {
	bar := Layer{ID: 1, Namespace: "bar", Layer: LayerTop, Output: "DP-1", Keyboard: KeyboardNone}
	wall := Layer{ID: 2, Namespace: "wallpaper", Layer: LayerBackground, Output: "DP-1"}
	tabs := &Group{ID: 5, Layout: "tab", Index: 0}
	a := Window{ID: 1, Group: &Group{ID: 4, Layout: "h"}}
	prev := Snapshot{Layers: []Layer{bar}, Windows: []Window{a}}
	a2 := a
	a2.Group, a2.Floating = tabs, true
	next := Snapshot{Layers: []Layer{wall}, Windows: []Window{a2}}
	want := []string{
		`{"event":"mode","window":1,"floating":true,"fullscreen":false,"maximized":false,"client_fullscreen":false}`,
		`{"event":"group","window":1,"group":{"id":5,"layout":"tab","index":0}}`,
		`{"event":"layer","change":"close","id":1,"namespace":"bar","output":"DP-1","layer":"top"}`,
		`{"event":"layer","change":"open","id":2,"namespace":"wallpaper","output":"DP-1","layer":"background"}`,
	}
	if got := names(Diff(prev, next)); !slices.Equal(got, want) {
		t.Fatalf("diff\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
	a3 := a2
	a3.Group = nil
	if got := names(Diff(next, Snapshot{Layers: []Layer{wall}, Windows: []Window{a3}})); !slices.Equal(got, []string{`{"event":"group","window":1,"group":null}`}) {
		t.Fatalf("leaving the group: %v", got)
	}
	id := wm.WindowID(3)
	if got, err := Encode(BellEvent{Window: &id}); err != nil || string(got) != `{"event":"bell","window":3}`+"\n" {
		t.Fatalf("bell %s, %v", got, err)
	}
}
