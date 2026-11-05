package harness

import (
	"fmt"
	"slices"
	"testing"

	ctix "github.com/stubbedev/lys-wm/internal/protocol/client/textinput"
	ctiv1 "github.com/stubbedev/lys-wm/internal/protocol/client/textinputv1"
	cw "github.com/stubbedev/lys-wm/internal/protocol/client/wayland"
	"github.com/stubbedev/lys-wm/internal/testclient"
)

type textInputV1Log struct{ log testclient.Log }

func (l *textInputV1Log) Enter(*ctiv1.ZwpTextInputV1, *cw.WlSurface) error { return l.log.Note("enter") }

func (l *textInputV1Log) Leave(*ctiv1.ZwpTextInputV1) error { return l.log.Note("leave") }

func (*textInputV1Log) ModifiersMap(*ctiv1.ZwpTextInputV1, []byte) error { return nil }

func (*textInputV1Log) InputPanelState(*ctiv1.ZwpTextInputV1, uint32) error { return nil }

func (l *textInputV1Log) PreeditString(_ *ctiv1.ZwpTextInputV1, serial uint32, text, commit string) error {
	return l.log.Note("preedit %d %q %q", serial, text, commit)
}

func (*textInputV1Log) PreeditStyling(*ctiv1.ZwpTextInputV1, uint32, uint32, ctiv1.ZwpTextInputV1PreeditStyle) error {
	return nil
}

func (l *textInputV1Log) PreeditCursor(_ *ctiv1.ZwpTextInputV1, index int32) error {
	return l.log.Note("cursor %d", index)
}

func (l *textInputV1Log) CommitString(_ *ctiv1.ZwpTextInputV1, serial uint32, text string) error {
	return l.log.Note("commit %d %q", serial, text)
}

func (*textInputV1Log) CursorPosition(*ctiv1.ZwpTextInputV1, int32, int32) error { return nil }

func (l *textInputV1Log) DeleteSurroundingText(_ *ctiv1.ZwpTextInputV1, index int32, length uint32) error {
	return l.log.Note("delete %d %d", index, length)
}

func (*textInputV1Log) Keysym(*ctiv1.ZwpTextInputV1, uint32, uint32, uint32, uint32, uint32) error {
	return nil
}

func (*textInputV1Log) Language(*ctiv1.ZwpTextInputV1, uint32, string) error { return nil }

func (*textInputV1Log) TextDirection(*ctiv1.ZwpTextInputV1, uint32, ctiv1.ZwpTextInputV1TextDirection) error {
	return nil
}

func TestAnInputMethodTypesIntoATextInputV1(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "chromium")
	mgr, err := ctiv1.NewZwpTextInputManagerV1(p.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.c.Bind(mgr)
	log := &textInputV1Log{}
	ti, err := mgr.CreateTextInput(log)
	if err != nil {
		t.Fatal(err)
	}
	m := inputMethod(t, h)
	flush := func(requests ...func() error) { send(t, append(requests, p.c.Display.Flush)...) }

	flush(func() error { return ti.SetSurroundingText("hello", 5, 5) }, func() error {
		return ti.SetContentType(ctiv1.ZwpTextInputV1ContentHintAutoCorrection, ctiv1.ZwpTextInputV1ContentPurposeTerminal)
	}, func() error { return ti.Activate(p.seat, p.w.Surface) })
	until(t, p.c, "the activated text input entered", &log.log, "enter")
	until(t, m.c, "the input method activated with the text input's state", &m.log.log,
		"activate", `surrounding "hello" 5 5`, fmt.Sprintf("content %d %d", ctix.ZwpTextInputV3ContentHintSpellcheck, ctix.ZwpTextInputV3ContentPurposeTerminal), "done")

	flush(func() error { return ti.CommitState(7) })
	until(t, m.c, "the committed state", &m.log.log, `surrounding "hello" 5 5`, fmt.Sprintf("content %d %d", ctix.ZwpTextInputV3ContentHintSpellcheck, ctix.ZwpTextInputV3ContentPurposeTerminal), "done")
	m.commit(t, func() error { return m.im.SetPreeditString("ni", 0, 2) })
	until(t, p.c, "the preedit", &log.log, "cursor 0", `preedit 7 "ni" ""`)
	m.commit(t, func() error { return m.im.DeleteSurroundingText(1, 0) }, func() error { return m.im.CommitString("你") })
	until(t, p.c, "the committed text", &log.log, "delete -1 1", `commit 7 "你"`)

	flush(func() error { return ti.Deactivate(p.seat) })
	until(t, p.c, "the deactivated text input left", &log.log, "leave")
	until(t, m.c, "the input method deactivated", &m.log.log, "deactivate", "done")

	flush(func() error { return ti.Activate(p.seat, p.w.Surface) })
	until(t, m.c, "the input method activated again", &m.log.log, "activate", `surrounding "hello" 5 5`, fmt.Sprintf("content %d %d", ctix.ZwpTextInputV3ContentHintSpellcheck, ctix.ZwpTextInputV3ContentPurposeTerminal), "done")
	other := join(t, h, "other")
	other.seen(t, "the other window focused", func() bool { return slices.Contains(other.in.Log, "kb_enter other") })
	until(t, p.c, "the text input left with the keyboard focus", &log.log, "enter", "leave")
	until(t, m.c, "the input method deactivated with the focus", &m.log.log, "deactivate", "done")
}

func TestATextInputV1ActivatingAnUnfocusedSurfaceGetsNothing(t *testing.T) {
	t.Parallel()
	h := Start(t, small)
	p := join(t, h, "behind")
	other := join(t, h, "front")
	other.seen(t, "the front window focused", func() bool { return slices.Contains(other.in.Log, "kb_enter front") })
	mgr, err := ctiv1.NewZwpTextInputManagerV1(p.c.Display, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.c.Bind(mgr)
	log := &textInputV1Log{}
	ti, err := mgr.CreateTextInput(log)
	if err != nil {
		t.Fatal(err)
	}
	m := inputMethod(t, h)
	send(t, func() error { return ti.Activate(p.seat, p.w.Surface) }, p.c.Display.Flush)
	p.c.Sync()
	m.c.Sync()
	if len(log.log) != 0 || len(m.log.log) != 0 {
		t.Fatalf("an unfocused activation gave the text input %q and the input method %q", log.log, m.log.log)
	}
}
