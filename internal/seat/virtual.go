package seat

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"

	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

type VirtualKeyboard struct {
	fd         int
	size       uint32
	mods       [4]uint32
	version    uint64
	keysyms    *xkb.ClientKeymap
	unreadable bool
}

type sentKeymap struct {
	km      *xkb.Keymap
	v       *VirtualKeyboard
	version uint64
}

func (s *Seat) sentFor(v *VirtualKeyboard) sentKeymap {
	if v == nil {
		return sentKeymap{km: s.keymap}
	}
	return sentKeymap{v: v, version: v.version}
}

func NewVirtualKeyboard() *VirtualKeyboard {
	return &VirtualKeyboard{fd: -1}
}

func (v *VirtualKeyboard) HasKeymap() bool {
	return v.fd >= 0
}

func (v *VirtualKeyboard) SetKeymap(fd int, size uint32) error {
	err := v.Close()
	v.fd, v.size = fd, size
	v.version++
	v.keysyms, v.unreadable = nil, false
	return err
}

func (v *VirtualKeyboard) Close() error {
	if v.fd < 0 {
		return nil
	}
	err := unix.Close(v.fd)
	v.fd = -1
	if err != nil {
		return fmt.Errorf("seat: close a virtual keymap: %w", err)
	}
	return nil
}

func (s *Seat) syncKeymap(kb *sw.WlKeyboard, v *VirtualKeyboard) (bool, error) {
	want := s.sentFor(v)
	if s.keymaps[kb] == want {
		return false, nil
	}
	s.keymaps[kb] = want
	if v == nil {
		return true, kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.keymap.FD(), s.keymap.Size())
	}
	return true, kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, v.fd, v.size)
}

func (s *Seat) useKeymap(kb *sw.WlKeyboard, v *VirtualKeyboard) error {
	sent, err := s.syncKeymap(kb, v)
	switch {
	case !sent || err != nil:
		return err
	case v == nil:
		return s.sendModifiers(kb)
	}
	return v.sendModifiers(s, kb)
}

func (v *VirtualKeyboard) sendModifiers(s *Seat, kb *sw.WlKeyboard) error {
	return kb.SendModifiers(s.display.NextSerial(), v.mods[0], v.mods[1], v.mods[2], v.mods[3])
}

func (s *Seat) focusedKeyboards() []*sw.WlKeyboard {
	if _, r := s.resourcesAt(s.receiver()); r != nil {
		return r.keyboards
	}
	return nil
}

func (s *Seat) VirtualKey(v *VirtualKeyboard, code uint32, pressed bool, at time.Duration) error {
	if !v.HasKeymap() {
		return errors.New("seat: a virtual key without a keymap")
	}
	return s.sendKey(v, code, pressed, false, at)
}

func (s *Seat) VirtualModifiers(v *VirtualKeyboard, depressed, latched, locked, group uint32) error {
	v.mods = [4]uint32{depressed, latched, locked, group}
	var errs error
	for _, kb := range s.focusedKeyboards() {
		if s.keymaps[kb].v == v {
			errs = errors.Join(errs, v.sendModifiers(s, kb))
		}
	}
	return errs
}

const maxVirtualKeymap = 1 << 22

func readKeymap(fd int, size uint32) ([]byte, error) {
	if size > maxVirtualKeymap {
		return nil, fmt.Errorf("seat: a virtual keymap of %d bytes, over the %d the compositor reads", size, maxVirtualKeymap)
	}
	text := make([]byte, size)
	for off := 0; off < len(text); {
		n, err := unix.Pread(fd, text[off:], int64(off))
		if err != nil {
			return nil, fmt.Errorf("seat: read a virtual keymap: %w", err)
		}
		if n == 0 {
			return nil, fmt.Errorf("seat: a virtual keymap ends at %d of its %d bytes", off, size)
		}
		off += n
	}
	return bytes.TrimRight(text, "\x00"), nil
}

func (s *Seat) compileVirtual(v *VirtualKeyboard) (*xkb.ClientKeymap, error) {
	text, err := readKeymap(v.fd, v.size)
	if err != nil {
		return nil, err
	}
	return s.cfg.Keymap.CompileClient(text)
}

func (s *Seat) virtualKeysyms(v *VirtualKeyboard) *xkb.ClientKeymap {
	if !v.HasKeymap() || v.unreadable {
		return nil
	}
	if v.keysyms == nil {
		km, err := s.compileVirtual(v)
		if err != nil {
			v.unreadable = true
			return nil
		}
		v.keysyms = km
	}
	return v.keysyms
}

func (v *VirtualKeyboard) modifiers() xkb.Modifiers {
	return xkb.Modifiers{Depressed: v.mods[0], Latched: v.mods[1], Locked: v.mods[2], Group: v.mods[3]}
}

func (s *Seat) VirtualBareEscape(v *VirtualKeyboard, code uint32) bool {
	km := s.virtualKeysyms(v)
	return km != nil && km.BareEscape(code, v.modifiers())
}

func (s *Seat) VirtualBareKeysym(v *VirtualKeyboard, code uint32) (uint32, bool) {
	km := s.virtualKeysyms(v)
	if km == nil || v.mods[0] != 0 || v.mods[1] != 0 {
		return 0, false
	}
	return km.Keysym(code, v.modifiers()), true
}
