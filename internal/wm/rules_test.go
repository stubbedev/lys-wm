package wm

import (
	"regexp"
	"testing"
)

func TestSteamRules(t *testing.T) {
	steam := regexp.MustCompile(`^steam$`)
	rules := []Rule{
		{Match: Match{AppID: steam}, Mode: RuleFloat},
		{Match: Match{AppID: steam, Title: regexp.MustCompile(`^Steam$`)}, Mode: RuleTile},
		{Match: Match{AppID: steam, Title: regexp.MustCompile(`^Friends List$`)}, Size: Px(400, 900)},
	}
	cases := []struct {
		app, title string
		want       Decision
	}{
		{"steam", "Steam", Decision{Mode: RuleTile}},
		{"steam", "Friends List", Decision{Mode: RuleFloat, Size: Px(400, 900)}},
		{"steam", "Steam Settings", Decision{Mode: RuleFloat}},
		{"foot", "Steam", Decision{}},
	}
	for _, c := range cases {
		if got := Decide(rules, Subject{AppID: c.app, Title: c.title}); got != c.want {
			t.Errorf("%s %q: %+v, want %+v", c.app, c.title, got, c.want)
		}
	}
}

func TestATagRuleAppliesToTheTaggedToplevelOnly(t *testing.T) {
	rules := []Rule{
		{Match: Match{AppID: regexp.MustCompile(`^org\.mozilla\.firefox$`)}, Mode: RuleTile},
		{Match: Match{Tag: regexp.MustCompile(`^picture-in-picture$`)}, Mode: RuleFloat, Size: Px(480, 270)},
	}
	if got := Decide(rules, Subject{AppID: "org.mozilla.firefox", Title: "Video", Tag: "picture-in-picture"}); got != (Decision{Mode: RuleFloat, Size: Px(480, 270)}) {
		t.Errorf("the tagged window: %+v", got)
	}
	if got := Decide(rules, Subject{AppID: "org.mozilla.firefox", Title: "Video", Tag: "main window"}); got != (Decision{Mode: RuleTile}) {
		t.Errorf("another tag: %+v", got)
	}
	if got := Decide(rules, Subject{AppID: "org.mozilla.firefox", Title: "picture-in-picture"}); got != (Decision{Mode: RuleTile}) {
		t.Errorf("an untagged window: %+v", got)
	}
}

func TestMatchersAndExclusions(t *testing.T) {
	yes, no := true, false
	m := Match{InitialTitle: regexp.MustCompile(`^Open`), Floating: &yes, Exclude: &Match{XWayland: &yes}}
	for _, tc := range []struct {
		s    Subject
		want bool
	}{
		{Subject{InitialTitle: "Open File", Floating: true}, true},
		{Subject{InitialTitle: "Open File", Title: "Save", Floating: true}, true},
		{Subject{Title: "Open File", Floating: true}, false},
		{Subject{InitialTitle: "Open File"}, false},
		{Subject{InitialTitle: "Open File", Floating: true, XWayland: true}, false},
	} {
		if got := m.Matches(tc.s); got != tc.want {
			t.Errorf("%+v: %t, want %t", tc.s, got, tc.want)
		}
	}
	if (Match{XWayland: &no}).Matches(Subject{XWayland: true}) {
		t.Error("xwayland: false matched an X window")
	}
}

func TestDecidingAllocatesNothing(t *testing.T) {
	rules := []Rule{
		{Match: Match{AppID: regexp.MustCompile(`^steam$`), Exclude: &Match{Title: regexp.MustCompile(`^Steam$`)}}, Mode: RuleFloat},
		{Match: Match{InitialTitle: regexp.MustCompile(`Friends`)}, Size: Px(400, 900)},
	}
	s := Subject{AppID: "steam", Title: "Friends List", InitialTitle: "Friends List"}
	if n := testing.AllocsPerRun(100, func() { Decide(rules, s) }); n != 0 {
		t.Fatalf("deciding a window's rules allocated %g times; regexps compile once at load", n)
	}
}
