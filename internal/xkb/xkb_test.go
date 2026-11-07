package xkb_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-wm/internal/testenv"
	"github.com/stubbedev/lys-wm/internal/xkb"
)

var layouts = xkb.Names{Layout: "us,dk,es", Options: "grp:toggle"}

func compile(t *testing.T) *xkb.Keymap {
	t.Helper()
	km, err := testenv.XKBCompiler(t).Compile(layouts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = km.Close() })
	return km
}

func state(t *testing.T) *xkb.State {
	t.Helper()
	return compile(t).NewState()
}

const (
	keyLeftShift = 42
	keyRightAlt  = 100
	keyA         = 30
)

func TestModifiersAndLayoutGroups(t *testing.T) {
	st := state(t)
	if !st.Key(keyLeftShift, true) || st.Modifiers().Depressed == 0 {
		t.Fatalf("shift press: %+v", st.Modifiers())
	}
	if !st.Key(keyLeftShift, false) || st.Modifiers().Depressed != 0 {
		t.Fatalf("shift release: %+v", st.Modifiers())
	}
	var groups []uint32
	for range 3 {
		st.Key(keyRightAlt, true)
		st.Key(keyRightAlt, false)
		groups = append(groups, st.Modifiers().Group)
	}
	if !slices.Equal(groups, []uint32{1, 2, 0}) {
		t.Fatalf("grp:toggle cycled through groups %v, want 1, 2, 0", groups)
	}
	if st.Key(keyA, true) {
		t.Fatal("pressing a letter reported a modifier change")
	}
}

func TestKeysymsFollowModifiers(t *testing.T) {
	st := state(t)
	const keyF1, keyLeftCtrl, keyLeftAlt = 59, 29, 56
	if got := st.Keysym(keyF1); got != 0xffbe {
		t.Fatalf("F1 is keysym %#x", got)
	}
	st.Key(keyLeftCtrl, true)
	st.Key(keyLeftAlt, true)
	if got := st.Keysym(keyF1); got != 0x1008fe01 {
		t.Fatalf("Ctrl+Alt+F1 is keysym %#x, want XF86Switch_VT_1", got)
	}
}

func TestLayoutNames(t *testing.T) {
	want := []string{"English (US)", "Danish", "Spanish"}
	if got := compile(t).Layouts(); !slices.Equal(got, want) {
		t.Fatalf("layouts %q, want %q", got, want)
	}
}

func TestKeymapFDIsSealedAndComplete(t *testing.T) {
	km := compile(t)
	seals, err := unix.FcntlInt(uintptr(km.FD()), unix.F_GET_SEALS, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL
	if seals&want != want {
		t.Fatalf("seals %#x, want %#x", seals, want)
	}
	m, err := unix.Mmap(km.FD(), 0, int(km.Size()), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(m)
	if string(m) != km.Text()+"\x00" {
		t.Fatal("the fd does not hold the NUL-terminated keymap text")
	}
	if _, err := unix.Write(km.FD(), []byte("x")); err == nil {
		t.Fatal("the keymap fd accepted a write")
	}
}

func TestInvalidNames(t *testing.T) {
	if _, err := testenv.XKBCompiler(t).Compile(xkb.Names{Layout: "no-such-layout"}); err == nil {
		t.Fatal("an unknown layout compiled")
	}
}

func TestEnvironment(t *testing.T) {
	env := map[string]string{"XKB_DEFAULT_LAYOUT": "us,dk", "XKB_DEFAULT_OPTIONS": "grp:toggle"}
	getenv := func(k string) string { return env[k] }
	if got := xkb.NamesFromEnv(getenv); got != (xkb.Names{Layout: "us,dk", Options: "grp:toggle"}) {
		t.Fatalf("names %+v", got)
	}
}

func TestLibxkbcommonClientsReadAllThreeLayouts(t *testing.T) {
	xkbcli := testenv.Require(t, "xkbcli")
	km := compile(t)
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(args ...string) string {
		var stderr bytes.Buffer
		cmd := exec.CommandContext(context.Background(), xkbcli, args...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("xkbcli %v: %v\n%s", args, err, stderr.String())
		}
		return string(out)
	}
	served := write("served.xkb", km.Text())
	once := run("compile-keymap", "--format", "1", "--keymap", served)
	twice := run("compile-keymap", "--format", "1", "--keymap", write("once.xkb", once))
	if once != twice {
		t.Fatal("re-serializing the served keymap through libxkbcommon is not stable")
	}
	for _, c := range []struct{ cp, key, layout string }{
		{"0xe6", "AC10", "Danish"},
		{"0xf8", "AC11", "Danish"},
		{"0xe5", "AD11", "Danish"},
		{"0xf1", "AC10", "Spanish"},
		{"0xe7", "BKSL", "Spanish"},
	} {
		out := run("how-to-type", "--format", "1", "--keymap="+served, c.cp)
		found := false
		for line := range strings.SplitSeq(out, "\n") {
			if f := strings.Fields(line); len(f) > 3 && f[1] == c.key && strings.Contains(line, c.layout) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not typed on %s in the %s layout:\n%s", c.cp, c.key, c.layout, out)
		}
	}
}

func TestKeycodesFromTheFirstLayout(t *testing.T) {
	km := compile(t)
	for name, want := range map[string]uint32{"q": 16, "Q": 16, "bracketleft": 26, "Return": 28, "exclam": 2, "XF86AudioRaiseVolume": 115} {
		got, err := km.Keycode(name)
		if err != nil || got != want {
			t.Errorf("Keycode(%q) = %d, %v; want %d", name, got, err, want)
		}
	}
	if _, err := km.Keycode("not_a_key"); !errors.Is(err, xkb.ErrUnknownKeysym) {
		t.Errorf("an unknown keysym: %v", err)
	}
	if _, err := km.Keycode("aring"); !errors.Is(err, xkb.ErrKeysymNotMapped) {
		t.Errorf("å is only on the dk layout: %v", err)
	}
	for _, name := range []string{"Shift", "Control", "Mod1", "Mod4"} {
		if m, err := km.ModMask(name); err != nil || m == 0 {
			t.Errorf("ModMask(%q) = %#x, %v", name, m, err)
		}
	}
	if _, err := km.ModMask("Hyper9"); !errors.Is(err, xkb.ErrUnknownModifier) {
		t.Errorf("an unknown modifier: %v", err)
	}
}

func TestLayoutsBeyondWhatTheRulesNameAreIgnoredAsInLibxkbcommon(t *testing.T) {
	names := xkb.Names{Layout: "us,dk,es,fr,de,it", Options: "grp:toggle"}
	km, err := testenv.XKBCompiler(t).Compile(names)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = km.Close() })
	const rulesLayouts = 4
	if got := len(km.Layouts()); got != rulesLayouts {
		t.Fatalf("%d layouts: %v", got, km.Layouts())
	}
	st := km.NewState()
	const keyToggle = 100
	seen := map[uint32]bool{}
	for range rulesLayouts {
		seen[st.Modifiers().Group] = true
		st.Key(keyToggle, true)
		st.Key(keyToggle, false)
	}
	if len(seen) != rulesLayouts || st.Modifiers().Group != 0 {
		t.Errorf("the toggle reached layouts %v and ended on %d", seen, st.Modifiers().Group)
	}
}

func TestNamesIgnoreTheEnvironment(t *testing.T) {
	env := map[string]string{"XKB_DEFAULT_LAYOUT": "dk", "XKB_DEFAULT_OPTIONS": "grp:toggle"}
	for _, k := range []string{"XKB_CONFIG_ROOT", "HOME"} {
		env[k] = os.Getenv(k)
	}
	km, err := xkb.NewCompiler(func(k string) string { return env[k] }).Compile(xkb.Names{Layout: "us"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = km.Close() })
	if got := km.Layouts(); !slices.Equal(got, []string{"English (US)"}) {
		t.Errorf("layouts %v: the environment filled in names", got)
	}
}

func TestNextLayoutCyclesAndWraps(t *testing.T) {
	st := state(t)
	var groups []uint32
	for range 4 {
		if !st.NextLayout() {
			t.Fatalf("no change from group %d", st.Modifiers().Group)
		}
		groups = append(groups, st.Modifiers().Group)
	}
	if !slices.Equal(groups, []uint32{1, 2, 0, 1}) {
		t.Fatalf("groups %v, want 1, 2, 0, 1", groups)
	}
}

func TestNextLayoutFollowsTheToggleKey(t *testing.T) {
	st := state(t)
	st.Key(keyRightAlt, true)
	st.Key(keyRightAlt, false)
	st.NextLayout()
	if got := st.Modifiers().Group; got != 2 {
		t.Fatalf("after the toggle and a next layout the group is %d, want 2", got)
	}
	st.Key(keyRightAlt, true)
	st.Key(keyRightAlt, false)
	if got := st.Modifiers().Group; got != 0 {
		t.Fatalf("the toggle after a next layout gives group %d, want 0", got)
	}
}

func TestNextLayoutWithOneLayoutChangesNothing(t *testing.T) {
	km, err := testenv.XKBCompiler(t).Compile(xkb.Names{Layout: "us"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = km.Close() })
	st := km.NewState()
	if st.NextLayout() || st.Modifiers().Group != 0 {
		t.Fatalf("one layout moved to group %d", st.Modifiers().Group)
	}
}

func TestUnshiftedKeysymsComeFromTheFirstLevelOfAnyLayout(t *testing.T) {
	km := compile(t)
	const keySemicolon, keyB = 39, 48
	for _, c := range []struct {
		code, sym uint32
		want      bool
	}{
		{keyB, 'b', true}, {keyB, 'B', false}, {keySemicolon, ';', true}, {keySemicolon, 0xe6, true}, {keySemicolon, 0xc6, false}, {keyA, 'b', false},
	} {
		if got := km.Unshifted(c.code, c.sym); got != c.want {
			t.Errorf("key %d unshifted %#x: %v, want %v", c.code, c.sym, got, c.want)
		}
	}
}

func TestOnlyKeysThatComposeTextTypeIt(t *testing.T) {
	for sym, want := range map[uint32]bool{
		'a': true, ' ': true, 0xff0d: true, 0xfe51: true,
		0xffbe: false, 0xffb1: false, 0xffeb: false, 0x1008ff14: false, 0xff51: false,
	} {
		if got := xkb.TypesText(sym); got != want {
			t.Errorf("keysym %#x types text: %v, want %v", sym, got, want)
		}
	}
	if !xkb.IsModifier(0xffeb) || xkb.IsModifier('a') {
		t.Error("Super_L is a modifier keysym and a is not")
	}
}

func TestLockNumLockLightsItsLEDAndTurnsTheKeypadToDigits(t *testing.T) {
	const keyKP1 = 79
	s := state(t)
	off := s.Keysym(keyKP1)
	if err := s.LockNumLock(); err != nil {
		t.Fatal(err)
	}
	if !s.Locks().Num || s.Modifiers().Locked == 0 {
		t.Fatalf("locks %+v modifiers %+v after locking Num Lock", s.Locks(), s.Modifiers())
	}
	if on := s.Keysym(keyKP1); on == off || on != 0xffb1 {
		t.Fatalf("KP_1 is %#x with Num Lock, %#x without", on, off)
	}
}
