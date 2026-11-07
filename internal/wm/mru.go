package wm

type Step uint8

const (
	StepNext Step = iota + 1
	StepPrev
)

var stepNames = []string{StepNext: "next", StepPrev: "prev"}

func (s Step) String() string { return enumName(stepNames, s, "Step") }

func (s Step) Delta() int {
	if s == StepPrev {
		return -1
	}
	return 1
}

type MRUScope uint8

const (
	ScopeAll MRUScope = iota
	ScopeApp
	ScopeWorkspace
	ScopeOutput
)

var scopeNames = []string{ScopeApp: "app", ScopeWorkspace: "workspace", ScopeOutput: "output"}

func (s MRUScope) suffix() string {
	if s == ScopeAll {
		return ""
	}
	return " " + enumName(scopeNames, s, "MRUScope")
}

type (
	FocusLastAction   struct{}
	FocusUrgentAction struct{}
	CycleAction       struct {
		Step  Step
		Scope MRUScope
	}
	SwitcherAction struct {
		Step  Step
		Scope MRUScope
	}
)

func (FocusLastAction) isEffect()   {}
func (FocusUrgentAction) isEffect() {}
func (CycleAction) isEffect()       {}
func (SwitcherAction) isEffect()    {}

func (FocusLastAction) String() string   { return "focuslast" }
func (FocusUrgentAction) String() string { return "focusurgent" }
func (a CycleAction) String() string     { return "cycle " + a.Step.String() + a.Scope.suffix() }
func (a SwitcherAction) String() string  { return "switcher " + a.Step.String() + a.Scope.suffix() }

func (a FocusLastAction) apply(s State, _ Settings) (State, []Effect, error)   { return emit(s, a) }
func (a FocusUrgentAction) apply(s State, _ Settings) (State, []Effect, error) { return emit(s, a) }
func (a CycleAction) apply(s State, _ Settings) (State, []Effect, error)       { return emit(s, a) }
func (a SwitcherAction) apply(s State, _ Settings) (State, []Effect, error)    { return emit(s, a) }

func parseStepAndScope(spec ActionSpec, args []string, scopes ...MRUScope) (Step, MRUScope, error) {
	if len(args) == 0 || len(args) > 2 {
		return 0, 0, badArgs(spec, "needs next or prev and an optional scope")
	}
	step, ok := parseEnum[Step](stepNames, args[0])
	if !ok {
		return 0, 0, badArgs(spec, "%q is not next or prev", args[0])
	}
	if len(args) == 1 {
		return step, ScopeAll, nil
	}
	scope, ok := parseEnum[MRUScope](scopeNames, args[1])
	for _, allowed := range scopes {
		if ok && scope == allowed {
			return step, scope, nil
		}
	}
	return 0, 0, badArgs(spec, "%q is not a scope it takes", args[1])
}
