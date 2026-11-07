package wm

const DefaultPeek = 2

type ShowDesktopAction struct {
	Switch Switch
	All    bool
}

func (ShowDesktopAction) isEffect() {}

func (a ShowDesktopAction) String() string {
	text := switched("showdesktop", a.Switch)
	if a.All {
		text += " all"
	}
	return text
}

func (a ShowDesktopAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func parseShowDesktop(spec ActionSpec, args []string, _ string) (Action, error) {
	all := len(args) > 0 && args[len(args)-1] == "all"
	if all {
		args = args[:len(args)-1]
	}
	sw, err := optionalSwitch(spec, args)
	return ShowDesktopAction{Switch: sw, All: all}, err
}
