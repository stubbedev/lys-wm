// Package xkb compiles keymaps and tracks modifier and layout state through
// lys-lib's xkb, and serves keymaps to clients as sealed memfds.
package xkb

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	lxkb "github.com/stubbedev/lys-lib/xkb"
	"golang.org/x/sys/unix"
)

const evdevOffset = 8

// Names selects a keymap by rules, model, layout, variant and options.
type Names = lxkb.Names

// NamesFromEnv reads the XKB_DEFAULT_* variables.
func NamesFromEnv(getenv func(string) string) Names { return lxkb.NamesFromEnv(getenv) }

// Compiler compiles keymaps against the include paths the environment names.
type Compiler struct {
	ctx *lxkb.Context
}

// NewCompiler reads the environment for where keymaps include files from.
func NewCompiler(getenv func(string) string) *Compiler {
	return &Compiler{ctx: lxkb.NewContextFlags(getenv, lxkb.ContextNoEnvironmentNames)}
}

// ledIndex is an LED's slot in a keymap, and whether the keymap has it.
type ledIndex struct {
	slot uint32
	ok   bool
}

func (m *Keymap) led(name string) ledIndex {
	slot, ok := m.keymap.LEDIndex(name)
	return ledIndex{slot: slot, ok: ok}
}

// Keymap is a compiled keymap and the sealed memfd clients read it from.
type Keymap struct {
	compiler *Compiler
	keymap   *lxkb.Keymap
	text     string
	fd       int
	size     uint32
	layouts  []string
	caps     ledIndex
	num      ledIndex
	scroll   ledIndex
}

// Compile compiles names, with the format and limits libxkbcommon's
// xkb_keymap_new_from_names has, and serves the keymap as xkb_v1 text.
func (c *Compiler) Compile(names Names) (*Keymap, error) {
	km, err := c.ctx.NewKeymapFromNames(names)
	if err != nil {
		return nil, fmt.Errorf("xkb: compile a keymap from %+v: %w", names, err)
	}
	text := km.Text()
	if len(text) >= math.MaxUint32 {
		return nil, fmt.Errorf("xkb: the keymap for %+v is %d bytes, too large for wl_keyboard", names, len(text))
	}
	fd, err := sealedMemfd(text + "\x00")
	if err != nil {
		return nil, err
	}
	layouts := make([]string, km.NumLayouts())
	for i := range layouts {
		layouts[i] = km.LayoutName(uint32(i))
	}
	k := &Keymap{compiler: c, keymap: km, text: text, fd: fd, size: uint32(len(text) + 1), layouts: layouts} //nolint:gosec // bounded by the check above
	k.caps, k.num, k.scroll = k.led("Caps Lock"), k.led("Num Lock"), k.led("Scroll Lock")
	return k, nil
}

func sealedMemfd(content string) (int, error) {
	fd, err := unix.MemfdCreate("lys-wm-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, fmt.Errorf("xkb: memfd_create: %w", err)
	}
	for b := []byte(content); len(b) > 0; {
		n, err := unix.Write(fd, b)
		if err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("xkb: write keymap: %w", err)
		}
		b = b[n:]
	}
	seals := unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("xkb: seal keymap: %w", err)
	}
	return fd, nil
}

func (k *Keymap) FD() int {
	return k.fd
}

func (k *Keymap) Size() uint32 {
	return k.size
}

func (k *Keymap) Layouts() []string {
	return slices.Clone(k.layouts)
}

func (k *Keymap) Text() string {
	return k.text
}

func (k *Keymap) Close() error {
	if err := unix.Close(k.fd); err != nil {
		return fmt.Errorf("xkb: close keymap fd %d: %w", k.fd, err)
	}
	return nil
}

var (
	ErrUnknownKeysym   = errors.New("xkb: unknown keysym")
	ErrKeysymNotMapped = errors.New("xkb: keysym not on the first layout")
	ErrUnknownModifier = errors.New("xkb: unknown modifier")
)

func (k *Keymap) Keycode(keysym string) (uint32, error) {
	sym := lxkb.KeysymFromName(keysym, lxkb.KeysymNoFlags)
	if sym == lxkb.KeyNoSymbol {
		sym = lxkb.KeysymFromName(keysym, lxkb.KeysymNameCaseInsensitive)
	}
	if sym == lxkb.KeyNoSymbol {
		return 0, fmt.Errorf("%w %q", ErrUnknownKeysym, keysym)
	}
	km := k.keymap
	lo, hi := km.MinKeycode(), km.MaxKeycode()
	for _, anyLevel := range []bool{false, true} {
		for key := lo; key <= hi; key++ {
			levels := km.NumLevels(key, 0)
			if !anyLevel {
				levels = min(levels, 1)
			}
			for level := range levels {
				if slices.Contains(km.KeySymsByLevel(key, 0, level), sym) && key >= evdevOffset {
					return uint32(key) - evdevOffset, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrKeysymNotMapped, keysym)
}

func (k *Keymap) Unshifted(evdev, keysym uint32) bool {
	key := lxkb.Keycode(evdev + evdevOffset)
	for layout := range k.keymap.NumLayoutsForKey(key) {
		if slices.Contains(k.keymap.KeySymsByLevel(key, layout, 0), lxkb.Keysym(keysym)) {
			return true
		}
	}
	return false
}

func IsModifier(keysym uint32) bool {
	return lxkb.KeysymIsModifier(lxkb.Keysym(keysym))
}

func TypesText(keysym uint32) bool {
	sym := lxkb.Keysym(keysym)
	if lxkb.KeysymIsModifier(sym) || lxkb.KeysymIsKeypad(sym) {
		return false
	}
	return strings.HasPrefix(lxkb.KeysymGetName(sym), "dead_") || lxkb.KeysymToUTF32(sym) != 0
}

func (k *Keymap) ModMask(name string) (uint32, error) {
	i, ok := k.keymap.ModIndex(name)
	if !ok || i >= 32 {
		return 0, fmt.Errorf("%w %q", ErrUnknownModifier, name)
	}
	return 1 << i, nil
}

type Modifiers struct {
	Depressed, Latched, Locked, Group uint32
}

type Locks struct {
	Caps, Num, Scroll bool
}

// State tracks one keyboard's modifiers, layout and LEDs over a Keymap.
type State struct {
	keymap *Keymap
	state  *lxkb.State
}

func (k *Keymap) NewState() *State {
	return &State{keymap: k, state: k.keymap.NewState()}
}

const reportedChanges = lxkb.StateModDepressed | lxkb.StateModLatched | lxkb.StateModLocked | lxkb.StateLayoutEffective

func (s *State) Key(evdev uint32, pressed bool) bool {
	dir := lxkb.KeyReleased
	if pressed {
		dir = lxkb.KeyPressed
	}
	return s.state.UpdateKey(lxkb.Keycode(evdev+evdevOffset), dir)&reportedChanges != 0
}

// NextLayout locks the keymap's next layout, wrapping after the last, and
// reports whether the layout changed: a keymap of one layout has no other.
func (s *State) NextLayout() bool {
	n := s.keymap.keymap.NumLayouts()
	if n < 2 {
		return false
	}
	locked := (s.state.SerializeLayout(lxkb.StateLayoutLocked) + 1) % n
	return s.state.UpdateLatchedLocked(0, 0, false, 0, 0, 0, true, int32(locked))&reportedChanges != 0 //nolint:gosec // below the keymap's layout count
}

const numLockModifier = "Mod2"

// LockNumLock locks the Num Lock modifier, as a press of Num Lock would.
func (s *State) LockNumLock() error {
	mask, err := s.keymap.ModMask(numLockModifier)
	if err != nil {
		return err
	}
	s.state.UpdateLatchedLocked(0, 0, false, 0, lxkb.ModMask(mask), lxkb.ModMask(mask), false, 0)
	return nil
}

func (s *State) Stick(mask uint32, before Modifiers) bool {
	m := s.Modifiers()
	latched, locked := m.Latched, m.Locked
	switch {
	case before.Locked&mask == mask:
		latched &^= mask
		locked &^= mask
	case before.Latched&mask == mask:
		latched &^= mask
		locked |= mask
	default:
		latched |= mask
	}
	affect := lxkb.ModMask(mask)
	return s.state.UpdateLatchedLocked(affect, lxkb.ModMask(latched), false, 0, affect, lxkb.ModMask(locked), false, 0)&reportedChanges != 0
}

func (s *State) Keysym(evdev uint32) uint32 {
	return uint32(s.state.KeyGetOneSym(lxkb.Keycode(evdev + evdevOffset)))
}

func (s *State) lit(l ledIndex) bool {
	return l.ok && s.state.LEDIndexIsActive(l.slot)
}

func (s *State) Locks() Locks {
	k := s.keymap
	return Locks{Caps: s.lit(k.caps), Num: s.lit(k.num), Scroll: s.lit(k.scroll)}
}

func (s *State) Modifiers() Modifiers {
	return Modifiers{
		Depressed: uint32(s.state.SerializeMods(lxkb.StateModDepressed)),
		Latched:   uint32(s.state.SerializeMods(lxkb.StateModLatched)),
		Locked:    uint32(s.state.SerializeMods(lxkb.StateModLocked)),
		Group:     s.state.SerializeLayout(lxkb.StateLayoutEffective),
	}
}

func bareEscape(s *lxkb.State, evdev uint32) bool {
	return s.KeyGetOneSym(lxkb.Keycode(evdev+evdevOffset)) == lxkb.KeyEscape &&
		s.SerializeMods(lxkb.StateModDepressed|lxkb.StateModLatched) == 0
}

// BareEscape reports whether evdev is Escape with no modifier depressed or
// latched; locked modifiers do not count.
func (s *State) BareEscape(evdev uint32) bool {
	return bareEscape(s.state, evdev)
}

// ClientKeymap is a keymap a client typed with, compiled only to read its
// keysyms.
type ClientKeymap struct {
	state *lxkb.State
}

// CompileClient compiles a client's xkb_v1 keymap text in the context k was
// compiled in.
func (k *Keymap) CompileClient(text []byte) (*ClientKeymap, error) {
	km, err := k.compiler.ctx.NewKeymapFromString(text)
	if err != nil {
		return nil, fmt.Errorf("xkb: compile a client keymap of %d bytes: %w", len(text), err)
	}
	return &ClientKeymap{state: km.NewState()}, nil
}

// BareEscape is State.BareEscape under a client's modifiers.
func (k *ClientKeymap) Keysym(evdev uint32, m Modifiers) uint32 {
	k.state.UpdateMask(lxkb.ModMask(m.Depressed), lxkb.ModMask(m.Latched), lxkb.ModMask(m.Locked), 0, 0, m.Group)
	return uint32(k.state.KeyGetOneSym(lxkb.Keycode(evdev + evdevOffset)))
}

func (k *ClientKeymap) BareEscape(evdev uint32, m Modifiers) bool {
	k.state.UpdateMask(lxkb.ModMask(m.Depressed), lxkb.ModMask(m.Latched), lxkb.ModMask(m.Locked), 0, 0, m.Group)
	return bareEscape(k.state, evdev)
}

// Keymaps is the seat's keymap and the keymaps devices of their own use;
// devices naming the same keymap share one compiled Keymap, so switching
// between them sends clients nothing.
type Keymaps struct {
	Seat    *Keymap
	Devices map[string]*Keymap
}

// CompileSet compiles the seat's names and each device's, once per distinct
// set of names.
func (c *Compiler) CompileSet(seat Names, devices map[string]Names) (Keymaps, error) {
	seatKeymap, err := c.Compile(seat)
	if err != nil {
		return Keymaps{}, err
	}
	set := Keymaps{Seat: seatKeymap}
	compiled := map[Names]*Keymap{seat: seatKeymap}
	for _, device := range slices.Sorted(maps.Keys(devices)) {
		names := devices[device]
		km, ok := compiled[names]
		if !ok {
			if km, err = c.Compile(names); err != nil {
				return Keymaps{}, errors.Join(fmt.Errorf("xkb: the keymap of input device %q: %w", device, err), set.Close())
			}
			compiled[names] = km
		}
		if set.Devices == nil {
			set.Devices = make(map[string]*Keymap, len(devices))
		}
		set.Devices[device] = km
	}
	return set, nil
}

// Close closes every distinct keymap in the set.
func (k Keymaps) Close() error {
	closed := map[*Keymap]bool{k.Seat: true}
	errs := []error{k.Seat.Close()}
	for _, device := range slices.Sorted(maps.Keys(k.Devices)) {
		km := k.Devices[device]
		if closed[km] {
			continue
		}
		closed[km] = true
		errs = append(errs, km.Close())
	}
	return errors.Join(errs...)
}

func KeysymRune(keysym uint32) rune {
	return lxkb.KeysymToUTF32(lxkb.Keysym(keysym))
}
