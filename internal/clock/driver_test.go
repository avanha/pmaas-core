package clock

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

// script drives a driver through the times given: each wait ends with the time being the next one on the list, and
// when the list is used up the driver is stopped. The first time is the one it starts at.
func script(t *testing.T, times ...time.Time) []string {
	t.Helper()

	current := times[0]
	remaining := times[1:]
	var got []string

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &driver{
		now: func() time.Time { return current },
		wait: func(_ context.Context, _ time.Duration) bool {
			if len(remaining) == 0 {
				return false
			}

			current, remaining = remaining[0], remaining[1:]

			return true
		},
		emit: func(event any) { got = append(got, describe(event)) },
	}

	d.run(ctx)

	return got
}

// describe is an event as the kind of change and the minute it's for: "hour 2026-01-01T10:00".
func describe(event any) string {
	name := strings.TrimSuffix(reflect.TypeOf(event).Name(), "ChangedEvent")
	at := reflect.ValueOf(event).FieldByName("Time").Interface().(time.Time)

	return fmt.Sprintf("%s %s", strings.ToLower(name), at.Format("2006-01-02T15:04"))
}

func at(zone *time.Location, value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", value, zone)
	if err != nil {
		panic(err)
	}

	return parsed
}

func zone(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}

	return location
}

func expect(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Errorf("events:\n got  %v\n want %v", got, want)
	}
}

func TestDriver_EachMinuteBroadcastsOnlyTheMinute(t *testing.T) {
	utc := time.UTC

	expect(t, script(t,
		at(utc, "2026-03-10T10:15:20"),
		at(utc, "2026-03-10T10:16:00"),
		at(utc, "2026-03-10T10:17:00"),
	), "minute 2026-03-10T10:16", "minute 2026-03-10T10:17")
}

func TestDriver_StartingDoesNotBroadcastTheMinuteItStartedIn(t *testing.T) {
	expect(t, script(t, at(time.UTC, "2026-03-10T10:15:59")))
}

func TestDriver_TheEventCarriesTheStartOfTheMinuteWhateverTheSecond(t *testing.T) {
	// A timer fires a little after the minute, not exactly on it.
	expect(t, script(t,
		at(time.UTC, "2026-03-10T10:15:20"),
		at(time.UTC, "2026-03-10T10:16:00").Add(37*time.Millisecond),
	), "minute 2026-03-10T10:16")
}

func TestDriver_AnHourChangeComesBeforeTheMinuteChangeItCameWith(t *testing.T) {
	expect(t, script(t,
		at(time.UTC, "2026-03-10T10:59:30"),
		at(time.UTC, "2026-03-10T11:00:00"),
	), "hour 2026-03-10T11:00", "minute 2026-03-10T11:00")
}

func TestDriver_ChangesComeLargestFirstAtTheEndOfTheYear(t *testing.T) {
	expect(t, script(t,
		at(time.UTC, "2026-12-31T23:59:30"),
		at(time.UTC, "2027-01-01T00:00:00"),
	),
		"year 2027-01-01T00:00",
		"month 2027-01-01T00:00",
		"day 2027-01-01T00:00",
		"hour 2027-01-01T00:00",
		"minute 2027-01-01T00:00")
}

func TestDriver_TheEndOfAMonthIsNotTheEndOfAYear(t *testing.T) {
	expect(t, script(t,
		at(time.UTC, "2026-01-31T23:59:30"),
		at(time.UTC, "2026-02-01T00:00:00"),
	),
		"month 2026-02-01T00:00",
		"day 2026-02-01T00:00",
		"hour 2026-02-01T00:00",
		"minute 2026-02-01T00:00")
}

func TestDriver_TheSameMonthAndDayInTheNextYearIsANewMonth(t *testing.T) {
	// A year slept through lands on the same month, day and hour. Only comparing the parts that changed would miss
	// all of it.
	expect(t, script(t,
		at(time.UTC, "2026-03-10T10:15:30"),
		at(time.UTC, "2027-03-10T10:15:00"),
	),
		"year 2027-03-10T10:15",
		"month 2027-03-10T10:15",
		"day 2027-03-10T10:15",
		"hour 2027-03-10T10:15",
		"minute 2027-03-10T10:15")
}

func TestDriver_AnyMinutesMissedAreOneEvent(t *testing.T) {
	// Asleep through three hours: told once about the time it finds, not about each of the 180 minutes.
	expect(t, script(t,
		at(time.UTC, "2026-03-10T10:15:30"),
		at(time.UTC, "2026-03-10T13:20:05"),
	), "hour 2026-03-10T13:20", "minute 2026-03-10T13:20")
}

func TestDriver_WakingBeforeTheMinuteHasChangedBroadcastsNothing(t *testing.T) {
	expect(t, script(t,
		at(time.UTC, "2026-03-10T10:15:30"),
		at(time.UTC, "2026-03-10T10:15:59").Add(900*time.Millisecond),
		at(time.UTC, "2026-03-10T10:16:00"),
	), "minute 2026-03-10T10:16")
}

func TestDriver_ClockSetBackBroadcastsNothingAndCarriesOn(t *testing.T) {
	expect(t, script(t,
		at(time.UTC, "2026-03-10T10:15:30"),
		at(time.UTC, "2026-03-10T09:00:00"),
		at(time.UTC, "2026-03-10T09:00:30"),
		at(time.UTC, "2026-03-10T09:01:00"),
	), "minute 2026-03-10T09:01")
}

func TestDriver_TheHourThatRepeatsWhenClocksGoBackIsAnotherHour(t *testing.T) {
	denver := zone("America/Denver")
	first := at(denver, "2026-11-01T00:59:30").Add(time.Hour) // 1:59:30 MDT, the first time it's 1:xx
	second := first.Add(30 * time.Second)                     // 1:00:00 MST, the second

	if first.Hour() != 1 || second.Hour() != 1 || first.Format("MST") != "MDT" || second.Format("MST") != "MST" {
		t.Fatalf("the test's times are wrong: %v then %v", first, second)
	}

	expect(t, script(t, first, second),
		"hour 2026-11-01T01:00", "minute 2026-11-01T01:00")
}

func TestDriver_TheHourThatIsSkippedWhenClocksGoForwardIsNotMissed(t *testing.T) {
	denver := zone("America/Denver")
	before := at(denver, "2026-03-08T01:59:30") // MST
	after := before.Add(30 * time.Second)       // 3:00 MDT

	if after.Hour() != 3 {
		t.Fatalf("the test's times are wrong: %v then %v", before, after)
	}

	expect(t, script(t, before, after), "hour 2026-03-08T03:00", "minute 2026-03-08T03:00")
}

func TestDriver_HoursBeginOnTheLocalHourInAZoneThatIsHalfAnHourOff(t *testing.T) {
	kolkata := zone("Asia/Kolkata") // UTC+5:30

	expect(t, script(t,
		at(kolkata, "2026-03-10T10:28:30"),
		at(kolkata, "2026-03-10T10:29:00"),
		at(kolkata, "2026-03-10T10:30:00"), // 05:00 UTC
		at(kolkata, "2026-03-10T10:59:00"),
		at(kolkata, "2026-03-10T11:00:00"),
	),
		"minute 2026-03-10T10:29",
		"minute 2026-03-10T10:30",
		"minute 2026-03-10T10:59",
		"hour 2026-03-10T11:00",
		"minute 2026-03-10T11:00")
}

func TestDriver_TheDayChangesAtLocalMidnightNotUTC(t *testing.T) {
	denver := zone("America/Denver")

	expect(t, script(t,
		at(denver, "2026-03-10T17:59:30"), // 23:59 UTC
		at(denver, "2026-03-10T18:00:00"), // 00:00 UTC the next day
	), "hour 2026-03-10T18:00", "minute 2026-03-10T18:00")
}

func TestDriver_StopsWhenToldTo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{})
	finished := make(chan struct{})

	d := &driver{
		now:  func() time.Time { return time.Now() },
		wait: func(ctx context.Context, d time.Duration) bool { close(waiting); <-ctx.Done(); return false },
		emit: func(any) { t.Error("an event was broadcast") },
	}

	go func() { d.run(ctx); close(finished) }()

	<-waiting
	cancel()
	<-finished
}

func TestUntilNextMinute(t *testing.T) {
	for _, c := range []struct {
		at   string
		want time.Duration
	}{
		{"2026-03-10T10:15:00", time.Minute},
		{"2026-03-10T10:15:20", 40 * time.Second},
		{"2026-03-10T10:15:59", time.Second},
	} {
		if got := untilNextMinute(at(time.UTC, c.at)); got != c.want {
			t.Errorf("at %s: got %v, want %v", c.at, got, c.want)
		}
	}
}

func TestWaitFor(t *testing.T) {
	if !waitFor(context.Background(), time.Millisecond) {
		t.Error("a wait that ran its course returned false")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if waitFor(cancelled, time.Hour) {
		t.Error("a wait for a stopped driver returned true")
	}
}
