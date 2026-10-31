package wm

import (
	"image"
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

type Rule struct {
	AppID     *regexp.Regexp
	Title     *regexp.Regexp
	Mode      RuleMode
	Size      image.Point
	Tearing   RuleTearing
	Opacity   *float32
	DimAround bool
}

type Decision struct {
	Mode      RuleMode
	Size      image.Point
	Tearing   RuleTearing
	Opacity   *float32
	DimAround bool
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
	}
	return d
}
