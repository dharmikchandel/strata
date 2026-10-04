// Package gen produces a steady, realistic stream of log lines for a running
// Strata server, and can check afterwards that none of the lines it was told
// were stored went missing.
//
// It exists to test the live path (sealing by age, compaction while data keeps
// arriving, searches on fresh data, shutdown and failure under load), which
// loading a file in one burst does not exercise.
package gen

import (
	"fmt"
	"math"
	"time"
)

// Schedule describes how many lines per second to send over time: a steady rate,
// plus optional periodic bursts (a deploy, a retry storm, a traffic spike).
//
// The send loop works from the cumulative count (how many lines should have been
// sent by now), not from "sleep, then send": if one iteration is late, the next
// one catches up, so the average rate holds. That is what makes the generator
// open-loop: a slow server does not quietly reduce the load.
type Schedule struct {
	Rate        float64       // steady lines per second, all sources together
	BurstFactor float64       // during a burst the rate is Rate * BurstFactor (1 = no bursts)
	BurstEvery  time.Duration // a burst ends at every multiple of this...
	BurstFor    time.Duration // ...and lasts this long
}

// Validate reports a schedule that cannot be followed.
func (s Schedule) Validate() error {
	switch {
	case !(s.Rate > 0) || math.IsInf(s.Rate, 0):
		return fmt.Errorf("rate must be a positive number of lines per second, got %v", s.Rate)
	case s.BurstFactor < 1:
		return fmt.Errorf("burst factor must be at least 1 (1 means no bursts), got %v", s.BurstFactor)
	case s.BurstFactor > 1 && (s.BurstEvery <= 0 || s.BurstFor <= 0):
		return fmt.Errorf("bursts need both a period and a length")
	case s.BurstFactor > 1 && s.BurstFor >= s.BurstEvery:
		return fmt.Errorf("a burst (%s) must be shorter than the period between bursts (%s)", s.BurstFor, s.BurstEvery)
	}
	return nil
}

// Peak is the highest rate the schedule reaches.
func (s Schedule) Peak() float64 {
	if s.BurstFactor > 1 {
		return s.Rate * s.BurstFactor
	}
	return s.Rate
}

// Cumulative returns how many lines should have been sent after t of running.
// A burst occupies the last BurstFor of every BurstEvery, so the first one comes
// after the system has had time to settle.
func (s Schedule) Cumulative(t time.Duration) float64 {
	sec := t.Seconds()
	if sec <= 0 {
		return 0
	}
	total := s.Rate * sec
	if s.BurstFactor > 1 && s.BurstEvery > 0 && s.BurstFor > 0 {
		every, dur := s.BurstEvery.Seconds(), s.BurstFor.Seconds()
		full := math.Floor(sec / every)
		rem := sec - full*every
		burstSeconds := full*dur + math.Max(0, rem-(every-dur))
		total += (s.BurstFactor - 1) * s.Rate * burstSeconds
	}
	return total
}
