package wm

type FocusOrigin uint8

const (
	FocusOther FocusOrigin = iota
	FocusKeyboard
	FocusClick
	FocusHover
)

type Stacking uint8

const (
	Raise Stacking = iota
	KeepStacking
)

type FollowsMouse uint8

const (
	FollowsMouseLoose FollowsMouse = iota
	FollowsMouseAlways
	FollowsMouseOff
)

type WarpPointer uint8

const (
	WarpOff WarpPointer = iota
	WarpOnKeyboardFocus
	WarpOnWorkspaceSwitch
)

type CloseFocus uint8

const (
	CloseFocusesNext CloseFocus = iota
	CloseFocusesUnderPointer
)

type FocusSettings struct {
	OnActivate   ActivatePolicy
	FollowsMouse FollowsMouse
	RaiseOnHover bool
	WarpPointer  WarpPointer
	OnClose      CloseFocus
}

func (f FocusSettings) Stacking(o FocusOrigin) Stacking {
	if o == FocusHover && !f.RaiseOnHover {
		return KeepStacking
	}
	return Raise
}
