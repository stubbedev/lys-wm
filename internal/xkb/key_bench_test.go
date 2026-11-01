package xkb_test

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/testenv"
)

func BenchmarkKeyEvent(b *testing.B) {
	km, err := testenv.XKBCompiler(b).Compile(layouts)
	if err != nil {
		b.Fatal(err)
	}
	st := km.NewState()
	b.ReportAllocs()
	for b.Loop() {
		st.Key(keyA, true)
		_ = st.Keysym(keyA)
		_ = st.Modifiers()
		_ = st.Locks()
		st.Key(keyA, false)
	}
}

func TestKeyEventAllocatesNothing(t *testing.T) {
	st := state(t)
	if n := testing.AllocsPerRun(100, func() {
		st.Key(keyA, true)
		_ = st.Keysym(keyA)
		_ = st.Modifiers()
		_ = st.Locks()
		st.Key(keyA, false)
	}); n != 0 {
		t.Errorf("a key event allocated %v times", n)
	}
}
