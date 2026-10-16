// Package clock is the server's clock: it works out when the minute, hour, day, month and year change, and
// broadcasts an event for each.
package clock

import (
	"context"
	"time"

	spiclock "github.com/avanha/pmaas-spi/clock"
	"github.com/avanha/pmaas-spi/events"
)

// driver turns the passing of time into events. It has no idea where time or events come from, which is what lets
// the tests run a year in an instant.
type driver struct {
	// now is the current time, in the zone the events should be in.
	now func() time.Time

	// wait returns after d, or at once, with false, when ctx is done.
	wait func(ctx context.Context, d time.Duration) bool

	// entity is what every event says it's from.
	entity events.EntityEvent

	// emit is where each event goes, in the order they're to be delivered.
	emit func(event any)
}

// untilNextMinute is how long it is from t to the start of the next minute.
func untilNextMinute(t time.Time) time.Duration {
	return t.Truncate(time.Minute).Add(time.Minute).Sub(t)
}

// startOfMinute is the start of the minute t is in.
func startOfMinute(t time.Time) time.Time {
	return t.Truncate(time.Minute)
}

// startOfHour is the start of the hour t is in. It's worked out from the minute, and not by truncating the time, so
// that it's right in a zone whose hours don't begin on the hour in UTC, and so that the second 1am of the day a
// zone goes back an hour is a different hour from the first.
func startOfHour(t time.Time) time.Time {
	return startOfMinute(t).Add(-time.Duration(t.Minute()) * time.Minute)
}

// run broadcasts events until ctx is done. The minute it starts in is the baseline, and doesn't produce any.
func (d *driver) run(ctx context.Context) {
	last := startOfMinute(d.now())

	for {
		if !d.wait(ctx, untilNextMinute(d.now())) {
			return
		}

		current := startOfMinute(d.now())

		switch {
		case current.Equal(last):
			// Woken before the minute really changed, which a timer can be by a hair when the clock has been
			// adjusted. Wait out what's left of it.
			continue
		case current.Before(last):
			// The clock was set back. Nothing has happened that anyone should be told about; carry on from
			// where it is now.
			last = current

			continue
		}

		d.broadcastChanges(last, current)
		last = current
	}
}

// broadcastChanges broadcasts what is different about current from previous, the largest change first.
func (d *driver) broadcastChanges(previous time.Time, current time.Time) {
	sameYear := previous.Year() == current.Year()
	sameMonth := sameYear && previous.Month() == current.Month()
	sameDay := sameMonth && previous.Day() == current.Day()
	sameHour := sameDay && startOfHour(previous).Equal(startOfHour(current))

	if !sameYear {
		d.emit(spiclock.YearChangedEvent{EntityEvent: d.entity, Time: current})
	}

	if !sameMonth {
		d.emit(spiclock.MonthChangedEvent{EntityEvent: d.entity, Time: current})
	}

	if !sameDay {
		d.emit(spiclock.DayChangedEvent{EntityEvent: d.entity, Time: current})
	}

	if !sameHour {
		d.emit(spiclock.HourChangedEvent{EntityEvent: d.entity, Time: current})
	}

	d.emit(spiclock.MinuteChangedEvent{EntityEvent: d.entity, Time: current})
}

// waitFor is the real wait: a timer, ended early by ctx.
func waitFor(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
