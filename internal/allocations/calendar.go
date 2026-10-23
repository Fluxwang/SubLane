package allocations

import (
	"regexp"
	"strconv"
	"time"
)

var resetClock = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

func validResetSchedule(c Config) bool {
	if c.Period == "durations" {
		return c.ResetTime == "" && c.ResetDay == 0
	}
	if c.ResetTime != "" && !resetClock.MatchString(c.ResetTime) {
		return false
	}
	if c.Period == "day" {
		return c.ResetDay == 0
	}
	return c.ResetDay >= 0 && c.ResetDay <= 31
}

func resetMinutes(c Config) int {
	if c.ResetTime == "" {
		return 0
	}
	hour, _ := strconv.Atoi(c.ResetTime[:2])
	minute, _ := strconv.Atoi(c.ResetTime[3:])
	return hour*60 + minute
}

func Window(now time.Time, c Config, location *time.Location) (int64, int64) {
	local := now.In(location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if c.Period == "month" {
		date = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	current := resetOn(date, c, location)
	if now.Before(current) {
		if c.Period == "month" {
			return resetOn(date.AddDate(0, -1, 0), c, location).Unix(), current.Unix()
		}
		return resetOn(date.AddDate(0, 0, -1), c, location).Unix(), current.Unix()
	}
	if c.Period == "month" {
		return current.Unix(), resetOn(date.AddDate(0, 1, 0), c, location).Unix()
	}
	return current.Unix(), resetOn(date.AddDate(0, 0, 1), c, location).Unix()
}

func resetOn(date time.Time, c Config, location *time.Location) time.Time {
	day := date.Day()
	if c.Period == "month" {
		day = max(1, c.ResetDay)
		last := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		// A missing 29th–31st resets on the month's last day, then resumes the chosen day next month.
		day = min(day, last)
	}
	return wallTime(date.Year(), date.Month(), day, resetMinutes(c), location)
}

func wallTime(year int, month time.Month, day, minute int, location *time.Location) time.Time {
	start := dayStart(year, month, day, location)
	next := time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
	end := dayStart(next.Year(), next.Month(), next.Day(), location)
	want := time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Add(time.Duration(minute) * time.Minute)
	// Walk offset segments in order so skipped times use the next valid instant and repeated times use the first occurrence.
	for segment := start; segment.Before(end); {
		_, offset := segment.In(location).Zone()
		_, boundary := segment.In(location).ZoneBounds()
		if boundary.IsZero() || boundary.After(end) {
			boundary = end
		}
		candidate := want.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(segment) {
			candidate = segment
		}
		if candidate.Before(boundary) {
			return candidate
		}
		segment = boundary
	}
	return end
}

func dayStart(year int, month time.Month, day int, location *time.Location) time.Time {
	date := func(v time.Time) bool {
		return v.Year() == year && v.Month() == month && v.Day() == day
	}
	guess := time.Date(year, month, day, 0, 0, 0, 0, location)
	if local := guess.In(location); date(local) && local.Hour() == 0 && local.Minute() == 0 && !date(guess.Add(-time.Second).In(location)) {
		return guess
	}
	// Midnight may be skipped or repeated. Find the first instant in the target civil day.
	base := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	lo, hi := base.Add(-48*time.Hour).Unix(), base.Add(48*time.Hour).Unix()
	for lo < hi {
		mid := lo + (hi-lo)/2
		local := time.Unix(mid, 0).In(location)
		if local.Year() > year || local.Year() == year && (local.Month() > month || local.Month() == month && local.Day() >= day) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return time.Unix(lo, 0)
}

type allocationWindow struct {
	kind       string
	start, end int64
	seconds    int64
	limit      int64
}

func durationWindow(now, anchor, seconds int64) allocationWindow {
	if now < anchor {
		return allocationWindow{start: anchor, end: anchor + seconds}
	}
	start := anchor + (now-anchor)/seconds*seconds
	return allocationWindow{start: start, end: start + seconds}
}

func effectiveWindows(rev Revision, now int64, location *time.Location) []allocationWindow {
	if rev.Config.Period == "durations" {
		windows := make([]allocationWindow, 0, len(rev.Config.Windows))
		for _, condition := range rev.Config.Windows {
			window := durationWindow(now, rev.EffectiveAt, condition.DurationSeconds)
			window.kind = "duration"
			window.seconds = condition.DurationSeconds
			window.limit = condition.Limit
			windows = append(windows, window)
		}
		return windows
	}
	start, end := Window(unix(now), rev.Config, location)
	return []allocationWindow{{kind: rev.Config.Period, start: max(start, rev.EffectiveAt), end: end}}
}

func nextEffective(c Config, now int64, location *time.Location, anchor int64) int64 {
	if keyMode(c) {
		return now
	}
	if c.Period == "durations" {
		longest := int64(0)
		for _, condition := range c.Windows {
			longest = max(longest, condition.DurationSeconds)
		}
		return durationWindow(now, anchor, longest).end
	}
	_, end := Window(unix(now), c, location)
	return end
}
