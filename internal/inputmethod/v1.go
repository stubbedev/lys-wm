package inputmethod

import (
	"errors"

	stix "github.com/stubbedev/lys-wm/internal/protocol/server/textinput"
	stiv1 "github.com/stubbedev/lys-wm/internal/protocol/server/textinputv1"
	sw "github.com/stubbedev/lys-wm/internal/protocol/server/wayland"
	"github.com/stubbedev/lys-wm/internal/server"
	"github.com/stubbedev/lys-wm/internal/surface"
	"github.com/stubbedev/lys-wm/internal/wire"
)

func (m *Manager) bindTextInputV1(c *server.Client, id wire.ObjectID, v uint32) error {
	_, err := stiv1.NewZwpTextInputManagerV1(c, id, v, textInputManagerV1{m, c})
	return err
}

type textInputManagerV1 struct {
	m *Manager
	c *server.Client
}

func (r textInputManagerV1) CreateTextInput(_ *stiv1.ZwpTextInputManagerV1, res *stiv1.ZwpTextInputV1) (stiv1.ZwpTextInputV1Handler, error) {
	v := &v1Input{ti: &textInput{m: r.m, c: r.c}, res: res}
	v.ti.out = v
	return v, r.m.add(v.ti, res)
}

type v1Input struct {
	ti      *textInput
	res     *stiv1.ZwpTextInputV1
	serial  uint32
	entered *surface.Surface
}

func (*v1Input) enter(*surface.Surface) error { return nil }

func (v *v1Input) leave(*surface.Surface) error {
	if v.entered == nil {
		return nil
	}
	v.entered = nil
	return v.res.SendLeave()
}

func (v *v1Input) relay(st imState) error {
	var errs []error
	if p := st.preedit; p != nil {
		errs = append(errs, v.res.SendPreeditCursor(p.begin), v.res.SendPreeditString(v.serial, p.text, ""))
	} else if st.commit == nil {
		errs = append(errs, v.res.SendPreeditString(v.serial, "", ""))
	}
	if st.before != 0 || st.after != 0 {
		errs = append(errs, v.res.SendDeleteSurroundingText(-int32(st.before), st.before+st.after)) //nolint:gosec // surrounding text is far below 2 GiB
	}
	if st.commit != nil {
		errs = append(errs, v.res.SendCommitString(v.serial, *st.commit))
	}
	return errors.Join(errs...)
}

func (v *v1Input) Activate(_ *stiv1.ZwpTextInputV1, _ *sw.WlSeat, wl *sw.WlSurface) error {
	s, err := surface.From(wl)
	if err != nil {
		return err
	}
	if s != v.ti.focus {
		return nil
	}
	var errs []error
	if v.entered != s {
		v.entered = s
		errs = append(errs, v.res.SendEnter(wl))
	}
	v.ti.pending.enabled = true
	return errors.Join(append(errs, v.ti.apply())...)
}

func (v *v1Input) Deactivate(*stiv1.ZwpTextInputV1, *sw.WlSeat) error {
	v.ti.pending.enabled = false
	return errors.Join(v.ti.apply(), v.leave(nil))
}

func (*v1Input) ShowInputPanel(*stiv1.ZwpTextInputV1) error { return nil }

func (*v1Input) HideInputPanel(*stiv1.ZwpTextInputV1) error { return nil }

func (*v1Input) Reset(*stiv1.ZwpTextInputV1) error { return nil }

func (v *v1Input) SetSurroundingText(_ *stiv1.ZwpTextInputV1, text string, cursor, anchor uint32) error {
	v.ti.pending.surrounding = &surrounding{text: text, cursor: int32(cursor), anchor: int32(anchor)} //nolint:gosec // byte offsets into a surrounding text
	return nil
}

func (v *v1Input) SetContentType(_ *stiv1.ZwpTextInputV1, hint stiv1.ZwpTextInputV1ContentHint, purpose stiv1.ZwpTextInputV1ContentPurpose) error {
	v.ti.pending.content = &contentType{hint: stix.ZwpTextInputV3ContentHint(hint), purpose: purposeV3(purpose)}
	return nil
}

func purposeV3(p stiv1.ZwpTextInputV1ContentPurpose) stix.ZwpTextInputV3ContentPurpose {
	if p >= stiv1.ZwpTextInputV1ContentPurposeDate {
		return stix.ZwpTextInputV3ContentPurpose(p) + stix.ZwpTextInputV3ContentPurposeDate - stix.ZwpTextInputV3ContentPurpose(stiv1.ZwpTextInputV1ContentPurposeDate)
	}
	return stix.ZwpTextInputV3ContentPurpose(p)
}

func (v *v1Input) SetCursorRectangle(_ *stiv1.ZwpTextInputV1, x, y, w, h int32) error {
	v.ti.pending.cursor = cursorRect(x, y, w, h)
	return nil
}

func (*v1Input) SetPreferredLanguage(*stiv1.ZwpTextInputV1, string) error { return nil }

func (v *v1Input) CommitState(_ *stiv1.ZwpTextInputV1, serial uint32) error {
	v.serial = serial
	return v.ti.apply()
}

func (*v1Input) InvokeAction(*stiv1.ZwpTextInputV1, uint32, uint32) error { return nil }
