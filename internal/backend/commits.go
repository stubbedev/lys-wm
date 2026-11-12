package backend

import (
	"fmt"
	"time"
)

const SustainedFor = time.Second

type Commits struct {
	failing bool
	since   time.Duration
	stalled bool
}

func (c *Commits) Failed(name string, now time.Duration, cause error) (stall error) {
	switch {
	case Fatal(cause):
		return nil
	case !c.failing:
		c.failing, c.since = true, now
		return nil
	case c.stalled || now-c.since < SustainedFor:
		return nil
	}
	c.stalled = true
	return fmt.Errorf("%s has taken no commit for %v and no longer counts as displaying: %w", name, now-c.since, cause)
}

func (c *Commits) Took(name string, now time.Duration) (recovery error) {
	switch {
	case !c.stalled:
		c.failing = false
		return nil
	case c.failing:
		c.failing, c.since = false, now
		return nil
	case now-c.since < SustainedFor:
		return nil
	}
	c.stalled = false
	return fmt.Errorf("%s has taken every commit for %v and counts as displaying again", name, now-c.since)
}

func (c *Commits) Stalled() bool {
	return c.stalled
}

type Stalling interface {
	Stalled() bool
}

func Displays(o Output) bool {
	s, ok := o.(Stalling)
	return o.State().Enabled() && (!ok || !s.Stalled())
}
