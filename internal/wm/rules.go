package wm

import (
	"image"
	"image/color"
	"regexp"
	"strings"
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
	XWayland, Floating         bool
}

type Match struct {
	AppID, Title, Tag          *regexp.Regexp
	InitialAppID, InitialTitle *regexp.Regexp
	XWayland, Floating         *bool
	Exclude                    *Match
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
		!matchFlag(m.XWayland, s.XWayland) || !matchFlag(m.Floating, s.Floating) {
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
}

func Decide(rules []Rule, s Subject) Decision {
	var d Decision
	for _, r := range rules {
		if r.Match.Matches(s) {
			d = d.then(r.Decision)
		}
	}
	return d
}

func (d Decision) then(r Decision) Decision {
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
