package clock

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	_ "time/tzdata"

	spiclock "github.com/avanha/pmaas-spi/clock"
)

// These run the real driver, with the real timers and the real clock, on the virtual clock of a testing/synctest bubble:
// a day passes in an instant, and exactly.

type recorded struct {
	mu     sync.Mutex
	events []any
}

func (r *recorded) add(event any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, event)
}

func (r *recorded) counts() (minutes, hours, days, months, years int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, event := range r.events {
		switch event.(type) {
		case spiclock.MinuteChangedEvent:
			minutes++
		case spiclock.HourChangedEvent:
			hours++
		case spiclock.DayChangedEvent:
			days++
		case spiclock.MonthChangedEvent:
			months++
		case spiclock.YearChangedEvent:
			years++
		}
	}

	return minutes, hours, days, months, years
}

// runFor starts the real driver at the given moment in the given zone, lets the duration pass, stops it, and returns
// what it broadcast.
func runFor(t *testing.T, zone string, start time.Time, duration time.Duration) *recorded {
	t.Helper()

	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}

	previous := time.Local
	time.Local = location

	t.Cleanup(func() { time.Local = previous })

	time.Sleep(time.Until(start))

	events := &recorded{}
	d := &driver{now: time.Now, wait: waitFor, emit: events.add}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup

	wg.Go(func() { d.run(ctx) })
	synctest.Wait()

	time.Sleep(duration)
	synctest.Wait()

	cancel()
	wg.Wait()

	return events
}

func TestDriver_RealTimersThroughAnOrdinaryDay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		denver, _ := time.LoadLocation("America/Denver")
		start := time.Date(2000, time.June, 21, 0, 0, 0, 0, denver)

		// A day to the second: the minute events at 0:01 through 23:59, and the next midnight's, which is the 1,440th.
		events := runFor(t, "America/Denver", start, 24*time.Hour)

		minutes, hours, days, months, years := events.counts()

		if minutes != 1440 || hours != 24 || days != 1 || months != 0 || years != 0 {
			t.Errorf("minutes %d, hours %d, days %d, months %d, years %d", minutes, hours, days, months, years)
		}

		// Each was for the start of its minute, on the dot, which a timer on the virtual clock makes exact.
		for i, event := range events.events {
			if minute, ok := event.(spiclock.MinuteChangedEvent); ok && minute.Time.Second() != 0 {
				t.Fatalf("event %d is for %v, not the start of a minute", i, minute.Time)
			}
		}
	})
}

func TestDriver_RealTimersThroughTheDayClocksGoForwardAndTheDayTheyGoBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		denver, _ := time.LoadLocation("America/Denver")

		// 2 April 2000: 2am doesn't exist, so the day is 23 hours long and has 23 hour changes, one of which is at 3am.
		forward := runFor(t, "America/Denver", time.Date(2000, time.April, 2, 0, 0, 0, 0, denver), 23*time.Hour)

		if minutes, hours, days, _, _ := forward.counts(); minutes != 23*60 || hours != 23 || days != 1 {
			t.Errorf("spring forward: minutes %d, hours %d, days %d", minutes, hours, days)
		}

		// 29 October 2000: 1am happens twice, so the day is 25 hours, and each hour is an hour change.
		back := runFor(t, "America/Denver", time.Date(2000, time.October, 29, 0, 0, 0, 0, denver), 25*time.Hour)

		if minutes, hours, days, _, _ := back.counts(); minutes != 25*60 || hours != 25 || days != 1 {
			t.Errorf("fall back: minutes %d, hours %d, days %d", minutes, hours, days)
		}
	})
}

func TestDriver_RealTimersThroughTheEndOfAYear(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		denver, _ := time.LoadLocation("America/Denver")

		// An hour either side of midnight on New Year's Eve: one event of each large kind, once, in order.
		events := runFor(t, "America/Denver", time.Date(2000, time.December, 31, 23, 0, 0, 0, denver), 2*time.Hour)

		minutes, hours, days, months, years := events.counts()

		if minutes != 120 || hours != 2 || days != 1 || months != 1 || years != 1 {
			t.Errorf("minutes %d, hours %d, days %d, months %d, years %d", minutes, hours, days, months, years)
		}

		// The year, month, day, hour and minute for the midnight come in that order, together.
		for i, event := range events.events {
			if year, ok := event.(spiclock.YearChangedEvent); ok {
				want := []any{
					spiclock.YearChangedEvent{Time: year.Time}, spiclock.MonthChangedEvent{Time: year.Time},
					spiclock.DayChangedEvent{Time: year.Time}, spiclock.HourChangedEvent{Time: year.Time},
					spiclock.MinuteChangedEvent{Time: year.Time},
				}

				for j, w := range want {
					if events.events[i+j] != w {
						t.Fatalf("event %d after the year change is %v, want %v", j, events.events[i+j], w)
					}
				}
			}
		}
	})
}
