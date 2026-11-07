// Package binds maps typed input chords to window manager actions, with
// submaps and bind flags.
package binds

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/lys-wm/internal/wm"
)

type Resolver interface {
	Keycode(keysym string) (uint32, error)
	ModMask(name string) (uint32, error)
}

type Trigger interface {
	fmt.Stringer
	trigger()
}

type triggerKind uint8

const (
	kindKey triggerKind = iota + 1
	kindButton
	kindScroll
	kindTap
)

type (
	Key       struct{ Code uint32 }
	Button    struct{ Code uint32 }
	ScrollDir uint8
	Scroll    struct{ Dir ScrollDir }
	Tap       struct{ Modifier ModifierSet }
)

const (
	ScrollUp ScrollDir = iota + 1
	ScrollDown
)

func (Key) trigger()    {}
func (Button) trigger() {}
func (Scroll) trigger() {}
func (Tap) trigger()    {}

func (k Key) String() string    { return fmt.Sprintf("key %d", k.Code) }
func (b Button) String() string { return fmt.Sprintf("mouse:%d", b.Code) }
func (s Scroll) String() string {
	if s.Dir == ScrollUp {
		return "mouse_up"
	}
	return "mouse_down"
}
func (t Tap) String() string { return "tap " + t.Modifier.String() }

type Chord struct {
	Mods    uint32
	Trigger Trigger
}

type Flags struct {
	Locked       bool
	Repeating    bool
	Release      bool
	Pass         bool
	InhibitProof bool
	LongPress    time.Duration
	Cooldown     time.Duration
}

func (f Flags) OnRelease() bool {
	return f.Release || f.LongPress > 0
}

type Bind struct {
	Submap      string
	Chord       Chord
	Text        string
	Description string
	Flags       Flags
	Action      wm.Action
}

var ErrBadChord = errors.New("bad chord")

var modifiers = map[string]string{
	"SUPER": "Mod4", "MOD4": "Mod4", "WIN": "Mod4", "LOGO": "Mod4",
	"SHIFT":   "Shift",
	"CTRL":    "Control",
	"CONTROL": "Control",
	"ALT":     "Mod1", "MOD1": "Mod1",
}

type ModifierSet uint8

const (
	ModShift ModifierSet = 1 << iota
	ModControl
	ModAlt
	ModSuper
)

var modifierBits = map[string]ModifierSet{"Shift": ModShift, "Control": ModControl, "Mod1": ModAlt, "Mod4": ModSuper}

var modifierNames = [...]struct {
	bit  ModifierSet
	name string
}{{ModSuper, "SUPER"}, {ModControl, "CTRL"}, {ModAlt, "ALT"}, {ModShift, "SHIFT"}}

func (s ModifierSet) String() string {
	var parts []string
	for _, m := range modifierNames {
		if s&m.bit != 0 {
			parts = append(parts, m.name)
		}
	}
	return strings.Join(parts, " + ")
}

var modifierKeysyms = map[string]ModifierSet{
	"SHIFT_L": ModShift, "SHIFT_R": ModShift,
	"CONTROL_L": ModControl, "CONTROL_R": ModControl,
	"ALT_L": ModAlt, "ALT_R": ModAlt,
	"SUPER_L": ModSuper, "SUPER_R": ModSuper,
}

func TapOf(keysym uint32) (Tap, bool) {
	switch keysym {
	case 0xffe1, 0xffe2:
		return Tap{ModShift}, true
	case 0xffe3, 0xffe4:
		return Tap{ModControl}, true
	case 0xffe9, 0xffea:
		return Tap{ModAlt}, true
	case 0xffeb, 0xffec:
		return Tap{ModSuper}, true
	}
	return Tap{}, false
}

func ParseModifiers(text string) (ModifierSet, error) {
	var set ModifierSet
	for p := range strings.SplitSeq(text, "+") {
		p = strings.TrimSpace(p)
		name, ok := modifiers[strings.ToUpper(p)]
		if !ok {
			return 0, fmt.Errorf("%w %q: unknown modifier %q", ErrBadChord, text, p)
		}
		set |= modifierBits[name]
	}
	return set, nil
}

func (s ModifierSet) Mask(r Resolver) (uint32, error) {
	var mask uint32
	for name, bit := range modifierBits {
		if s&bit == 0 {
			continue
		}
		m, err := r.ModMask(name)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	return mask, nil
}

func Relevant(r Resolver) (uint32, error) {
	var mask uint32
	for _, name := range []string{"Shift", "Control", "Mod1", "Mod4"} {
		m, err := r.ModMask(name)
		if err != nil {
			return 0, err
		}
		mask |= m
	}
	return mask, nil
}

func tapNamed(name string) (Tap, bool) {
	upper := strings.ToUpper(name)
	if mod, ok := modifiers[upper]; ok {
		return Tap{modifierBits[mod]}, true
	}
	if bit, ok := modifierKeysyms[upper]; ok {
		return Tap{bit}, true
	}
	return Tap{}, false
}

func ParseChord(text string, r Resolver) (Chord, error) {
	parts := strings.Split(text, "+")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	last := parts[len(parts)-1]
	if last == "" {
		return Chord{}, fmt.Errorf("%w %q: no key", ErrBadChord, text)
	}
	var c Chord
	for _, p := range parts[:len(parts)-1] {
		name, ok := modifiers[strings.ToUpper(p)]
		if !ok {
			return Chord{}, fmt.Errorf("%w %q: unknown modifier %q", ErrBadChord, text, p)
		}
		m, err := r.ModMask(name)
		if err != nil {
			return Chord{}, fmt.Errorf("%w %q: %w", ErrBadChord, text, err)
		}
		c.Mods |= m
	}
	if t, ok := tapNamed(last); ok {
		c.Trigger = t
		return c, nil
	}
	switch lower := strings.ToLower(last); {
	case lower == "mouse_up":
		c.Trigger = Scroll{ScrollUp}
	case lower == "mouse_down":
		c.Trigger = Scroll{ScrollDown}
	case strings.HasPrefix(lower, "mouse:"):
		code, err := strconv.ParseUint(last[len("mouse:"):], 10, 32)
		if err != nil {
			return Chord{}, fmt.Errorf("%w %q: bad button %q", ErrBadChord, text, last)
		}
		c.Trigger = Button{uint32(code)}
	default:
		code, err := r.Keycode(last)
		if err != nil {
			return Chord{}, fmt.Errorf("%w %q: %w", ErrBadChord, text, err)
		}
		c.Trigger = Key{code}
	}
	return c, nil
}

type Gate uint8

const (
	GateOpen Gate = iota
	GateInhibited
	GateLocked
)

func (f Flags) passes(g Gate) bool {
	switch g {
	case GateOpen:
		return true
	case GateInhibited:
		return f.Locked || f.InhibitProof
	case GateLocked:
		return f.Locked
	}
	return false
}

type slot struct {
	submap string
	mods   uint32
	kind   triggerKind
	code   uint32
}

func slotOf(submap string, mods uint32, t Trigger) slot {
	s := slot{submap: submap, mods: mods}
	switch t := t.(type) {
	case Key:
		s.kind, s.code = kindKey, t.Code
	case Button:
		s.kind, s.code = kindButton, t.Code
	case Scroll:
		s.kind, s.code = kindScroll, uint32(t.Dir)
	case Tap:
		s.kind, s.code = kindTap, uint32(t.Modifier)
	}
	return s
}

type index struct {
	slots   map[slot]int
	lookups uint64
}

type Map struct {
	binds    []Bind
	relevant uint32
	index    *index
}

func (m Map) Held(depressed uint32) uint32 {
	return depressed & m.relevant
}

func Duplicates(binds []Bind) [][2]int {
	type key struct {
		submap string
		chord  Chord
	}
	first := map[key]int{}
	var dups [][2]int
	for i, b := range binds {
		k := key{b.Submap, b.Chord}
		if j, ok := first[k]; ok {
			dups = append(dups, [2]int{j, i})
			continue
		}
		first[k] = i
	}
	return dups
}

var ErrBadFlags = errors.New("bad bind flags")

func (b Bind) Normalized() (Bind, error) {
	if _, tap := b.Chord.Trigger.(Tap); tap {
		b.Flags.Release = true
	}
	f := b.Flags
	_, scroll := b.Chord.Trigger.(Scroll)
	switch {
	case f.LongPress < 0 || f.Cooldown < 0:
		return Bind{}, fmt.Errorf("%w: %s: long_press and cooldown cannot be negative", ErrBadFlags, b.Action)
	case f.OnRelease() && f.Repeating:
		return Bind{}, fmt.Errorf("%w: %s: a bind that fires on release cannot repeat", ErrBadFlags, b.Action)
	case f.OnRelease() && scroll:
		return Bind{}, fmt.Errorf("%w: %s: a scroll has no release", ErrBadFlags, b.Action)
	}
	return b, nil
}

func NewMap(binds []Bind, relevant uint32) (Map, error) {
	if dups := Duplicates(binds); len(dups) > 0 {
		a, b := binds[dups[0][0]], binds[dups[0][1]]
		return Map{}, fmt.Errorf("binds: %s and %s share a chord in submap %q", a.Action, b.Action, b.Submap)
	}
	normal := make([]Bind, len(binds))
	slots := make(map[slot]int, len(binds))
	for i, b := range binds {
		n, err := b.Normalized()
		if err != nil {
			return Map{}, fmt.Errorf("binds: %w", err)
		}
		normal[i] = n
		slots[slotOf(n.Submap, n.Chord.Mods&relevant, n.Chord.Trigger)] = i
	}
	return Map{binds: normal, relevant: relevant, index: &index{slots: slots}}, nil
}

func (m Map) Match(submap string, mods uint32, t Trigger, g Gate) (Bind, int, bool) {
	if m.index == nil {
		return Bind{}, 0, false
	}
	m.index.lookups++
	i, ok := m.index.slots[slotOf(submap, mods&m.relevant, t)]
	if !ok || !m.binds[i].Flags.passes(g) {
		return Bind{}, 0, false
	}
	return m.binds[i], i, true
}

func (m Map) Lookups() uint64 {
	if m.index == nil {
		return 0
	}
	return m.index.lookups
}

func (m Map) Len() int {
	return len(m.binds)
}

func (m Map) All() []Bind {
	return slices.Clone(m.binds)
}

func (m Map) Any(f func(Bind) bool) bool {
	return slices.ContainsFunc(m.binds, f)
}

func (m Map) At(i int) Bind {
	return m.binds[i]
}

var flagSwitches = [...]struct {
	name string
	on   func(*Flags) *bool
}{
	{"locked", func(f *Flags) *bool { return &f.Locked }},
	{"repeating", func(f *Flags) *bool { return &f.Repeating }},
	{"release", func(f *Flags) *bool { return &f.Release }},
	{"pass", func(f *Flags) *bool { return &f.Pass }},
	{"inhibit_proof", func(f *Flags) *bool { return &f.InhibitProof }},
}

func FlagNames() []string {
	out := make([]string, len(flagSwitches))
	for i, s := range flagSwitches {
		out[i] = s.name
	}
	return out
}

func (f *Flags) Set(name string) bool {
	for _, s := range flagSwitches {
		if s.name == name {
			*s.on(f) = true
			return true
		}
	}
	return false
}

func (f Flags) Names() []string {
	out := []string{}
	for _, s := range flagSwitches {
		if *s.on(&f) {
			out = append(out, s.name)
		}
	}
	return out
}
