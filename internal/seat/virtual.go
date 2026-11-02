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
	v       *VirtualKeyboard
	version uint64
}

func (v *VirtualKeyboard) sent() sentKeymap {
	if v == nil {
		return sentKeymap{}
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

func (s *Seat) useKeymap(kb *sw.WlKeyboard, v *VirtualKeyboard) error {
	if s.keymaps[kb] == v.sent() {
		return nil
	}
	s.keymaps[kb] = v.sent()
	if v == nil {
		return errors.Join(kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, s.cfg.Keymap.FD(), s.cfg.Keymap.Size()), s.sendModifiers(kb))
	}
	return errors.Join(kb.SendKeymap(sw.WlKeyboardKeymapFormatXkbV1, v.fd, v.size), v.sendModifiers(s, kb))
}

func (v *VirtualKeyboard) sendModifiers(s *Seat, kb *sw.WlKeyboard) error {
	return kb.SendModifiers(s.display.NextSerial(), v.mods[0], v.mods[1], v.mods[2], v.mods[3])
}

func (s *Seat) focusedKeyboards() []*sw.WlKeyboard {
	if _, r := s.resourcesAt(s.keyboard.focus); r != nil {
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
	kbs := s.focusedKeyboards()
	var errs []error
	for _, kb := range kbs {
		if s.keymaps[kb].v == v {
			errs = append(errs, v.sendModifiers(s, kb))
		}
	}
	return errors.Join(errs...)
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

func (s *Seat) VirtualBareEscape(v *VirtualKeyboard, code uint32) bool {
	if !v.HasKeymap() || v.unreadable {
		return false
	}
	if v.keysyms == nil {
		km, err := s.compileVirtual(v)
		if err != nil {
			v.unreadable = true
			return false
		}
		v.keysyms = km
	}
	return v.keysyms.BareEscape(code, xkb.Modifiers{Depressed: v.mods[0], Latched: v.mods[1], Locked: v.mods[2], Group: v.mods[3]})
}
