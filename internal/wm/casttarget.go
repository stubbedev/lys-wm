package wm

import "strconv"

type CastKind uint8

const (
	CastWindow CastKind = iota + 1
	CastOutput
	CastClear
)

var castKinds = []string{CastWindow: "window", CastOutput: "output", CastClear: "clear"}

func (k CastKind) String() string {
	return enumName(castKinds, k, "CastKind")
}

type CastTargetAction struct {
	Kind   CastKind
	Window WindowID
	Output string
}

func (CastTargetAction) isEffect() {}

func (a CastTargetAction) String() string {
	switch {
	case a.Kind == CastWindow && a.Window != 0:
		return "casttarget window " + strconv.FormatUint(uint64(a.Window), 10)
	case a.Kind == CastOutput && a.Output != "":
		return "casttarget output " + a.Output
	}
	return "casttarget " + a.Kind.String()
}

func (a CastTargetAction) apply(s State, _ Settings) (State, []Effect, error) {
	return emit(s, a)
}

func parseCastTarget(spec ActionSpec, args []string, _ string) (Action, error) {
	if len(args) == 0 || len(args) > 2 {
		return nil, badArgs(spec, "takes window, output or clear and at most one more argument, not %d arguments", len(args))
	}
	kind, ok := parseEnum[CastKind](castKinds, args[0])
	switch {
	case !ok:
		return nil, badArgs(spec, "%q is not window, output or clear", args[0])
	case len(args) == 1:
		return CastTargetAction{Kind: kind}, nil
	case kind == CastClear:
		return nil, badArgs(spec, "clear takes nothing more, not %q", args[1])
	case kind == CastOutput:
		return CastTargetAction{Kind: kind, Output: args[1]}, nil
	}
	id, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil || id == 0 {
		return nil, badArgs(spec, "window ID %q is not a positive number", args[1])
	}
	return CastTargetAction{Kind: kind, Window: WindowID(id)}, nil
}
