package gles

import (
	"testing"

	"github.com/stubbedev/lys-wm/internal/testenv"
)

func TestCloseDeletesEveryProgram(t *testing.T) {
	d, err := testenv.GPU(t).Surfaceless()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	r, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint32
	for _, p := range r.programs() {
		if p.prog.id != 0 {
			ids = append(ids, p.prog.id)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Enter(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if d.GL().IsProgram(id) {
			t.Errorf("program %d outlived the renderer", id)
		}
	}
	if err := d.Release(); err != nil {
		t.Fatal(err)
	}
}
