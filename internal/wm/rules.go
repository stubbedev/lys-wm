package wm

import (
	"image"
	"image/color"
	"regexp"
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

type Rule struct {
	AppID     *regexp.Regexp
	Title     *regexp.Regexp
	Mode      RuleMode
	Size      image.Point
	Tearing   RuleTearing
	Opacity   *float32
	DimAround bool
	Blur      *bool
	Look      LookOverride
	Mica      *bool
}

type Decision struct {
	Mode      RuleMode
	Size      image.Point
	Tearing   RuleTearing
	Opacity   *float32
	DimAround bool
	Blur      *bool
	Look      LookOverride
	Mica      *bool
}

func (r Rule) matches(appID, title string) bool {
	return (r.AppID == nil || r.AppID.MatchString(appID)) && (r.Title == nil || r.Title.MatchString(title))
}

func Decide(rules []Rule, appID, title string) Decision {
	var d Decision
	for _, r := range rules {
		if !r.matches(appID, title) {
			continue
		}
		if r.Mode != RuleUnset {
			d.Mode = r.Mode
		}
		if r.Size != (image.Point{}) {
			d.Size = r.Size
		}
		if r.Tearing != TearingUnset {
			d.Tearing = r.Tearing
		}
		if r.Opacity != nil {
			d.Opacity = r.Opacity
		}
		d.DimAround = d.DimAround || r.DimAround
		if r.Blur != nil {
			d.Blur = r.Blur
		}
		d.Look = d.Look.then(r.Look)
		if r.Mica != nil {
			d.Mica = r.Mica
		}
	}
	return d
}
