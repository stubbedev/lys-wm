package anim

import "maps"

var (
	moveSpring     = mustSpring(380, 34)
	openSpring     = mustSpring(420, 30)
	closeSpring    = mustSpring(700, 48)
	minimizeSpring = mustSpring(220, 30)
	switchSpring   = mustSpring(300, 34)
	layerInSpring  = mustSpring(500, 42)
	layerOutSpring = mustSpring(700, 50)
	fadeSpring     = mustSpring(600, 49)

	defaultOpen  = Style{Kind: Popin, Percent: 92}
	defaultClose = Style{Kind: Popin, Percent: 94}
	layerFade    = Style{Kind: FadeStyle}
	off          = false

	defaultMotion = map[Event]Setting{
		WindowsMove:     {Shape: moveSpring},
		WindowsIn:       {Shape: openSpring, Style: &defaultOpen},
		WindowsOut:      {Shape: closeSpring, Style: &defaultClose},
		WindowsMinimize: {Shape: minimizeSpring},
		Fade:            {Shape: fadeSpring},
		FadeOut:         {Shape: closeSpring},
		Workspaces:      {Shape: switchSpring},
		LayersIn:        {Shape: layerInSpring, Style: &layerFade},
		LayersOut:       {Shape: layerOutSpring, Style: &layerFade},
		Border:          {Shape: fadeSpring},
		BorderAngle:     {Enabled: &off},
	}
)

func DefaultMotion() map[Event]Setting {
	return maps.Clone(defaultMotion)
}

func FrugalMotion() map[Event]Setting {
	m := DefaultMotion()
	m[Workspaces] = Setting{Enabled: &off}
	return m
}

func Over(set, motion map[Event]Setting) map[Event]Setting {
	out := make(map[Event]Setting, eventCount)
	var enabledSet, shapeSet, durationSet [eventCount]bool
	for _, e := range Events() {
		user, def := set[e], motion[e]
		p := events[e].parent
		enabledSet[e] = user.Enabled != nil || (e != Global && enabledSet[p])
		shapeSet[e] = user.Shape != nil || (e != Global && shapeSet[p])
		durationSet[e] = user.Duration != nil || (e != Global && durationSet[p])
		s := user
		if s.Enabled == nil && !enabledSet[e] {
			s.Enabled = def.Enabled
		}
		if s.Shape == nil && !shapeSet[e] {
			s.Shape = def.Shape
			if durationSet[e] {
				s.Shape = EaseOut
			}
		}
		if s.Style == nil {
			s.Style = def.Style
		}
		out[e] = s
	}
	return out
}

func MustResolve(motion map[Event]Setting) Table {
	t, errs := Resolve(true, motion)
	if len(errs) != 0 {
		panic(errs)
	}
	return t
}
