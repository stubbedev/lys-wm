package binds

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/wm"
)

type fakeKeymap struct{}

var fakeMods = map[string]uint32{"Shift": 1, "Control": 4, "Mod1": 8, "Mod4": 64}

func (fakeKeymap) ModMask(name string) (uint32, error) {
	if m, ok := fakeMods[name]; ok {
		return m, nil
	}
	return 0, fmt.Errorf("no modifier %s", name)
}

func (fakeKeymap) Keycode(name string) (uint32, error) {
	codes := map[string]uint32{"Q": 16, "q": 16, "Return": 28, "bracketleft": 26}
	if c, ok := codes[name]; ok {
		return c, nil
	}
	return 0, fmt.Errorf("no keysym %s", name)
}

func TestParseChord(t *testing.T) {
	cases := map[string]Chord{
		"SUPER + SHIFT + Q":   {Mods: 65, Trigger: Key{16}},
		"super+q":             {Mods: 64, Trigger: Key{16}},
		"CTRL + ALT + Return": {Mods: 12, Trigger: Key{28}},
		"SUPER + mouse:272":   {Mods: 64, Trigger: Button{272}},
		"SUPER + mouse_down":  {Mods: 64, Trigger: Scroll{ScrollDown}},
		"bracketleft":         {Trigger: Key{26}},
		"SUPER":               {Trigger: Tap{ModSuper}},
		"Super_L":             {Trigger: Tap{ModSuper}},
		"CTRL + alt":          {Mods: 4, Trigger: Tap{ModAlt}},
	}
	for text, want := range cases {
		got, err := ParseChord(text, fakeKeymap{})
		if err != nil || got != want {
			t.Errorf("ParseChord(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
	for _, bad := range []string{"SUPER +", "HYPER + Q", "SUPER + nope", "SUPER + mouse:x"} {
		if _, err := ParseChord(bad, fakeKeymap{}); !errors.Is(err, ErrBadChord) {
			t.Errorf("ParseChord(%q) = %v, want ErrBadChord", bad, err)
		}
	}
}

func TestMatch(t *testing.T) {
	kill := Bind{Chord: Chord{Mods: 65, Trigger: Key{16}}, Action: wm.KillAction{}}
	volume := Bind{Chord: Chord{Trigger: Key{115}}, Flags: Flags{Locked: true}, Action: wm.ExecAction{Command: "vol +"}}
	resizeExit := Bind{Submap: "resize", Chord: Chord{Trigger: Key{28}}, Action: wm.SubmapAction{}}
	m, err := NewMap([]Bind{kill, volume, resizeExit}, 1|4|8|64)
	if err != nil {
		t.Fatal(err)
	}
	const capsAndNum = 2 | 16
	if b, _, ok := m.Match("", 65|capsAndNum, Key{16}, GateOpen); !ok || b.Action != (wm.KillAction{}) {
		t.Fatal("caps and num lock got in the way of a bind")
	}
	if _, _, ok := m.Match("", 64, Key{16}, GateOpen); ok {
		t.Fatal("a chord matched with a modifier missing")
	}
	if _, _, ok := m.Match("", 0, Key{28}, GateOpen); ok {
		t.Fatal("a submap bind matched outside its submap")
	}
	if _, _, ok := m.Match("resize", 0, Key{28}, GateOpen); !ok {
		t.Fatal("a submap bind did not match inside its submap")
	}
	if _, _, ok := m.Match("", 65, Key{16}, GateLocked); ok {
		t.Fatal("a bind without the locked flag matched while locked")
	}
	if _, _, ok := m.Match("", 0, Key{115}, GateLocked); !ok {
		t.Fatal("a locked bind did not match while locked")
	}
	if _, err := NewMap([]Bind{kill, kill}, 0xff); err == nil {
		t.Fatal("two binds on one chord were accepted")
	}
}

func TestGatesAndFlags(t *testing.T) {
	inhibitProof := Bind{Chord: Chord{Trigger: Key{1}}, Flags: Flags{InhibitProof: true}, Action: wm.KillAction{}}
	plain := Bind{Chord: Chord{Trigger: Key{2}}, Action: wm.KillAction{}}
	locked := Bind{Chord: Chord{Trigger: Key{3}}, Flags: Flags{Locked: true}, Action: wm.KillAction{}}
	m, err := NewMap([]Bind{inhibitProof, plain, locked}, 0xff)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		code uint32
		gate Gate
		want bool
	}{
		{1, GateInhibited, true},
		{1, GateLocked, false},
		{2, GateInhibited, false},
		{2, GateOpen, true},
		{3, GateInhibited, true},
		{3, GateLocked, true},
	} {
		if _, _, ok := m.Match("", 0, Key{c.code}, c.gate); ok != c.want {
			t.Errorf("key %d behind gate %d matched %t", c.code, c.gate, ok)
		}
	}
	if n := m.Lookups(); n != 6 {
		t.Errorf("six matches did %d lookups", n)
	}
	for _, bad := range []Bind{
		{Chord: Chord{Trigger: Key{1}}, Flags: Flags{Release: true, Repeating: true}, Action: wm.KillAction{}},
		{Chord: Chord{Trigger: Key{1}}, Flags: Flags{LongPress: time.Second, Repeating: true}, Action: wm.KillAction{}},
		{Chord: Chord{Trigger: Scroll{ScrollUp}}, Flags: Flags{Release: true}, Action: wm.KillAction{}},
		{Chord: Chord{Trigger: Key{1}}, Flags: Flags{Cooldown: -time.Second}, Action: wm.KillAction{}},
	} {
		if _, err := NewMap([]Bind{bad}, 0xff); !errors.Is(err, ErrBadFlags) {
			t.Errorf("flags %+v on %s: %v, want ErrBadFlags", bad.Flags, bad.Chord.Trigger, err)
		}
	}
}

func TestModifierKeysymsAreTaps(t *testing.T) {
	for keysym, want := range map[uint32]ModifierSet{0xffeb: ModSuper, 0xffec: ModSuper, 0xffe9: ModAlt, 0xffe3: ModControl, 0xffe2: ModShift} {
		if got, ok := TapOf(keysym); !ok || got.Modifier != want {
			t.Errorf("keysym %#x: %v %t, want %v", keysym, got, ok, want)
		}
	}
	if _, ok := TapOf('a'); ok {
		t.Error("a letter is a modifier tap")
	}
}

func TestATapFiresOnRelease(t *testing.T) {
	m, err := NewMap([]Bind{{Chord: Chord{Trigger: Tap{ModSuper}}, Action: wm.KillAction{}}}, 0xff)
	if err != nil {
		t.Fatal(err)
	}
	if b, _, ok := m.Match("", 0, Tap{ModSuper}, GateOpen); !ok || !b.Flags.Release {
		t.Fatalf("a modifier tap %+v, matched %t; want it released-fired", b.Flags, ok)
	}
	if _, err := NewMap([]Bind{{Chord: Chord{Trigger: Tap{ModSuper}}, Flags: Flags{Repeating: true}, Action: wm.KillAction{}}}, 0xff); !errors.Is(err, ErrBadFlags) {
		t.Fatalf("a repeating tap: %v", err)
	}
}
