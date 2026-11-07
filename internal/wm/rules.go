package wm

import (
	"fmt"
	"image"
	"image/color"
	"regexp"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-wm/internal/anim"
	"github.com/stubbedev/lys-wm/internal/render"
)

type RuleMode uint8

const (
	RuleUnset RuleMode = iota
	RuleFloat
	RuleTile
)

type RuleTearing uint8

const (
	TearingUnset RuleTearing = iota
	TearingAllow
	TearingDeny
)

type Refraction struct {
	Bezel, Amount int
}

type Specular struct {
	Strength float32
	Width    int
}

type Look struct {
	Tint                                            color.RGBA
	SaturationShift, BrightnessShift, ContrastShift float32
	Grain                                           float32
	Refraction                                      Refraction
	Specular                                        Specular
}

type LookOverride struct {
	Tint                                            *color.RGBA
	SaturationShift, BrightnessShift, ContrastShift *float32
	Grain                                           *float32
	Refraction                                      *Refraction
	Specular                                        *Specular
}

func (o LookOverride) Over(l Look) Look {
	set := func(dst *float32, v *float32) {
		if v != nil {
			*dst = *v
		}
	}
	if o.Tint != nil {
		l.Tint = *o.Tint
	}
	set(&l.SaturationShift, o.SaturationShift)
	set(&l.BrightnessShift, o.BrightnessShift)
	set(&l.ContrastShift, o.ContrastShift)
	set(&l.Grain, o.Grain)
	if o.Refraction != nil {
		l.Refraction = *o.Refraction
	}
	if o.Specular != nil {
		l.Specular = *o.Specular
	}
	return l
}

func (o LookOverride) then(later LookOverride) LookOverride {
	pick := func(a, b *float32) *float32 {
		if b != nil {
			return b
		}
		return a
	}
	if later.Tint != nil {
		o.Tint = later.Tint
	}
	o.SaturationShift = pick(o.SaturationShift, later.SaturationShift)
	o.BrightnessShift = pick(o.BrightnessShift, later.BrightnessShift)
	o.ContrastShift = pick(o.ContrastShift, later.ContrastShift)
	o.Grain = pick(o.Grain, later.Grain)
	if later.Refraction != nil {
		o.Refraction = later.Refraction
	}
	if later.Specular != nil {
		o.Specular = later.Specular
	}
	return o
}

type ActivatePolicy uint8

const (
	ActivateFocus ActivatePolicy = iota
	ActivateUrgent
	ActivateIgnore
)

type Subject struct {
	AppID, Title, Tag          string
	InitialAppID, InitialTitle string
	XWayland                   bool
	State                      WindowState
}

type WindowState struct {
	Floating, Focused, Fullscreen, Urgent, Captured bool
}

type Match struct {
	AppID, Title, Tag          *regexp.Regexp
	InitialAppID, InitialTitle *regexp.Regexp
	XWayland                   *bool
	Floating, Focused          *bool
	Fullscreen, Urgent         *bool
	Captured                   *bool
	Exclude                    *Match
}

func (m Match) HasState() bool {
	return m.Floating != nil || m.Focused != nil || m.Fullscreen != nil || m.Urgent != nil || m.Captured != nil || m.Exclude != nil && m.Exclude.HasState()
}

func matchText(re *regexp.Regexp, s string) bool {
	return re == nil || re.MatchString(s)
}

func matchFlag(want *bool, is bool) bool {
	return want == nil || *want == is
}

func (m Match) Matches(s Subject) bool {
	if !matchText(m.AppID, s.AppID) || !matchText(m.Title, s.Title) || !matchText(m.Tag, s.Tag) ||
		!matchText(m.InitialAppID, s.InitialAppID) || !matchText(m.InitialTitle, s.InitialTitle) ||
		!matchFlag(m.XWayland, s.XWayland) || !matchFlag(m.Floating, s.State.Floating) || !matchFlag(m.Focused, s.State.Focused) ||
		!matchFlag(m.Fullscreen, s.State.Fullscreen) || !matchFlag(m.Urgent, s.State.Urgent) || !matchFlag(m.Captured, s.State.Captured) {
		return false
	}
	return m.Exclude == nil || !m.Exclude.Matches(s)
}

type Rule struct {
	Match Match
	Decision
}

type Decision struct {
	Mode       RuleMode
	Size       Extent
	Tearing    RuleTearing
	Opacity    *float32
	DimAround  bool
	Blur       *bool
	Look       LookOverride
	Mica       *bool
	OnActivate *ActivatePolicy
	Content    *ContentType
	Workspace  *WorkspacePlacement
	Output     *OutputMatch
	Focus      *bool
	Fullscreen *bool
	Maximized  *bool
	Position   Position
	Group      *Layout
	Suppress   Suppress
	Pin        *bool
	Props
}

type IdleInhibit uint8

const (
	IdleInhibitUnset IdleInhibit = iota
	IdleInhibitFocus
	IdleInhibitFullscreen
	IdleInhibitVisible
)

type WindowAnimation struct {
	Off     bool
	In, Out anim.Style
}

type Props struct {
	ActiveBorder, InactiveBorder *render.Border
	BorderWidth                  *int
	Rounding                     *int
	Shadow                       *bool
	DimInactive                  *bool
	Animation                    *WindowAnimation
	IdleInhibit                  IdleInhibit
	VRR                          *bool
	ScrollFactor                 *float64
	MinSize, MaxSize             image.Point
	KeepAspect                   *bool
	BlockOutFrom                 BlockFrom
}

func (p Props) merged(r Props) Props {
	p.ActiveBorder = pick(p.ActiveBorder, r.ActiveBorder)
	p.InactiveBorder = pick(p.InactiveBorder, r.InactiveBorder)
	p.BorderWidth = pick(p.BorderWidth, r.BorderWidth)
	p.Rounding = pick(p.Rounding, r.Rounding)
	p.Shadow = pick(p.Shadow, r.Shadow)
	p.DimInactive = pick(p.DimInactive, r.DimInactive)
	p.Animation = pick(p.Animation, r.Animation)
	if r.IdleInhibit != IdleInhibitUnset {
		p.IdleInhibit = r.IdleInhibit
	}
	p.VRR = pick(p.VRR, r.VRR)
	p.ScrollFactor = pick(p.ScrollFactor, r.ScrollFactor)
	if r.MinSize != (image.Point{}) {
		p.MinSize = r.MinSize
	}
	if r.MaxSize != (image.Point{}) {
		p.MaxSize = r.MaxSize
	}
	p.KeepAspect = pick(p.KeepAspect, r.KeepAspect)
	if r.BlockOutFrom != BlockNone {
		p.BlockOutFrom = r.BlockOutFrom
	}
	return p
}

func Decide(rules []Rule, s Subject) Decision {
	var d Decision
	for _, r := range rules {
		if r.Match.Matches(s) {
			d = d.Then(r.Decision)
		}
	}
	return d
}

func (d Decision) Then(r Decision) Decision {
	if r.Mode != RuleUnset {
		d.Mode = r.Mode
	}
	if r.Size.Set() {
		d.Size = r.Size
	}
	if r.Tearing != TearingUnset {
		d.Tearing = r.Tearing
	}
	d.Opacity = pick(d.Opacity, r.Opacity)
	d.DimAround = d.DimAround || r.DimAround
	d.Blur = pick(d.Blur, r.Blur)
	d.Look = d.Look.then(r.Look)
	d.Mica = pick(d.Mica, r.Mica)
	d.OnActivate = pick(d.OnActivate, r.OnActivate)
	d.Content = pick(d.Content, r.Content)
	d.Workspace = pick(d.Workspace, r.Workspace)
	d.Output = pick(d.Output, r.Output)
	d.Focus = pick(d.Focus, r.Focus)
	d.Fullscreen = pick(d.Fullscreen, r.Fullscreen)
	d.Maximized = pick(d.Maximized, r.Maximized)
	d.Group = pick(d.Group, r.Group)
	d.Pin = pick(d.Pin, r.Pin)
	if r.Position.Kind != PositionUnset {
		d.Position = r.Position
	}
	d.Suppress |= r.Suppress
	d.Props = d.merged(r.Props)
	return d
}

type Length struct {
	N       int
	Percent bool
}

func (l Length) Of(span int) int {
	if l.Percent {
		return span * l.N / 100
	}
	return l.N
}

type Extent struct {
	W, H Length
}

func Px(w, h int) Extent {
	return Extent{W: Length{N: w}, H: Length{N: h}}
}

func (e Extent) Set() bool {
	return e != Extent{}
}

func (e Extent) In(area image.Point) image.Point {
	return image.Pt(e.W.Of(area.X), e.H.Of(area.Y))
}

type PositionKind uint8

const (
	PositionUnset PositionKind = iota
	PositionCenter
	PositionCursor
	PositionAt
)

type Position struct {
	Kind PositionKind
	X, Y Length
}

type WorkspacePlacement struct {
	ID     WorkspaceID
	Silent bool
}

type OutputMatch struct {
	Name, Description string
}

func (m OutputMatch) Matches(name, description string) bool {
	if m.Description != "" {
		return strings.HasPrefix(description, m.Description)
	}
	return m.Name == name
}

type Suppress uint8

const (
	SuppressFullscreen Suppress = 1 << iota
	SuppressMaximize
	SuppressActivate
)

func (s Suppress) Has(x Suppress) bool {
	return s&x != 0
}

func pick[T any](earlier, later *T) *T {
	if later != nil {
		return later
	}
	return earlier
}

type Decider struct {
	gen         uint64
	static      Subject
	matched     []bool
	state       WindowState
	decided     bool
	d           Decision
	Evaluations int
}

func (k *Decider) Decide(rules []Rule, gen uint64, s Subject, override *Decision) (*Decision, bool) {
	static := s
	static.State = WindowState{}
	restatic := gen != k.gen || static != k.static || len(k.matched) != len(rules)
	if restatic {
		k.gen, k.static = gen, static
		k.matched = k.matched[:0]
		for _, r := range rules {
			k.matched = append(k.matched, !r.Match.HasState() && r.Match.Matches(static))
		}
	}
	if !restatic && k.decided && s.State == k.state {
		return &k.d, false
	}
	k.state, k.decided = s.State, true
	var d Decision
	for i, r := range rules {
		matched := k.matched[i]
		if r.Match.HasState() {
			k.Evaluations++
			matched = r.Match.Matches(s)
		}
		if matched {
			d = d.Then(r.Decision)
		}
	}
	if override != nil {
		d = d.Then(*override)
	}
	changed := d != k.d
	k.d = d
	return &k.d, changed
}

func (k *Decider) Forget() {
	k.decided = false
}

type SetPropAction struct {
	ID         WindowID
	Key, Value string
}

func (SetPropAction) isEffect() {}

func (a SetPropAction) String() string {
	return "setprop " + strconv.FormatUint(uint64(a.ID), 10) + " " + a.Key + " " + a.Value
}

func (a SetPropAction) apply(s State, _ Settings) (State, []Effect, error) {
	if _, ok := s.WorkspaceOf(a.ID); !ok {
		return State{}, nil, fmt.Errorf("set a property of window %d: %w", a.ID, ErrUnknownWindow)
	}
	return emit(s, a)
}

func parseSetProp(spec ActionSpec, args []string, rest string) (Action, error) {
	if len(args) < 3 {
		return nil, badArgs(spec, "takes a window ID, a rule key and a value")
	}
	id, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil || id == 0 {
		return nil, badArgs(spec, "window ID %q is not a positive number", args[0])
	}
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(rest, args[0])), args[1]))
	return SetPropAction{ID: WindowID(id), Key: args[1], Value: value}, nil
}

type BlockFrom uint8

const (
	BlockNone BlockFrom = iota
	BlockScreencast
	BlockCapture
)

func (b BlockFrom) Blocks(screencast bool) bool {
	return b == BlockCapture || b == BlockScreencast && screencast
}

type LayerProps struct {
	BlockOutFrom BlockFrom
}

func (p LayerProps) then(r LayerProps) LayerProps {
	if r.BlockOutFrom != BlockNone {
		p.BlockOutFrom = r.BlockOutFrom
	}
	return p
}

type LayerRule struct {
	Namespace *regexp.Regexp
	LayerProps
}

func DecideLayer(rules []LayerRule, namespace string) LayerProps {
	var p LayerProps
	for _, r := range rules {
		if r.Namespace.MatchString(namespace) {
			p = p.then(r.LayerProps)
		}
	}
	return p
}
