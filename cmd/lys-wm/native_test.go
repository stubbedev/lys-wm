package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-wm/internal/loop"
)

func TestPrimaryGPUFollowsTheRule(t *testing.T) {
	igpu := gpuCard{name: "card0", render: true}
	dgpu := gpuCard{name: "card1", render: true, bootVGA: true}
	vkms := gpuCard{name: "card2"}
	cases := []struct {
		name       string
		cards      []gpuCard
		configured string
		want       int
		why        string
	}{
		{"the config wins", []gpuCard{igpu, dgpu}, "card0", 0, "set by gpu in the config"},
		{"then the boot VGA device", []gpuCard{igpu, dgpu}, "", 1, "the boot VGA device"},
		{"a boot VGA device without a render node is skipped", []gpuCard{{name: "card0", bootVGA: true}, igpu}, "", 1, "the first card with a render node"},
		{"then the first render node", []gpuCard{vkms, igpu}, "", 1, "the first card with a render node"},
		{"display-only cards render in software as a last resort", []gpuCard{vkms}, "", 0, "the first card; none has a render node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why, err := choosePrimary(tc.cards, tc.configured)
			if err != nil || got != tc.want || !strings.HasPrefix(why, tc.why) {
				t.Fatalf("choosePrimary = %d, %q, %v", got, why, err)
			}
		})
	}
	if _, _, err := choosePrimary([]gpuCard{igpu}, "card7"); err == nil || !strings.Contains(err.Error(), "card7 is not a DRM card on this seat (card0)") {
		t.Fatalf("a configured card that is missing: %v", err)
	}
	if _, _, err := choosePrimary(nil, ""); err == nil {
		t.Fatal("no cards chose a primary")
	}
}

func TestTheLidPolicyRunsOnceTheWakeHoldoffPasses(t *testing.T) {
	l, err := loop.New()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	n := &native{l: l}
	var checks []time.Time
	n.onPower(func() {
		checks = append(checks, time.Now())
		l.Stop()
	})
	holdoff := 50 * time.Millisecond
	if err := n.recheckAfterWake(holdoff); err != nil {
		t.Fatal(err)
	}
	deadline, err := l.AddTimer(func() error {
		l.Stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func(limit time.Duration) {
		t.Helper()
		if err := deadline.Arm(limit); err != nil {
			t.Fatal(err)
		}
		if err := l.Run(); err != nil {
			t.Fatal(err)
		}
	}

	woke := time.Now()
	if err := errors.Join(n.PrepareForSleep(true), n.PrepareForSleep(false)); err != nil {
		t.Fatal(err)
	}
	run(5 * time.Second)
	if len(checks) != 1 || checks[0].Sub(woke) < holdoff {
		t.Fatalf("checks %v after waking at %v, want one after the %v holdoff", checks, woke, holdoff)
	}

	if err := errors.Join(n.PrepareForSleep(false), n.PrepareForSleep(true)); err != nil {
		t.Fatal(err)
	}
	run(4 * holdoff)
	if len(checks) != 1 {
		t.Fatalf("a sleep that began during the holdoff still ran the check: %d checks", len(checks))
	}
}
