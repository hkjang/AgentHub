package execution

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed five-field cron expression:
//
//	minute hour day-of-month month day-of-week
//
// It supports `*`, `a`, `a-b`, `a,b,c` and `*/n` (also `a-b/n`), which covers
// what an operator writes in practice. A dedicated parser avoids adding a
// dependency to an offline product and keeps timezone handling explicit.
type Schedule struct {
	minutes    map[int]bool
	hours      map[int]bool
	days       map[int]bool
	months     map[int]bool
	weekdays   map[int]bool
	daysStar   bool
	weekdayAny bool
}

// ParseSchedule validates a cron expression.
func ParseSchedule(expression string) (*Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron 표현식은 5개 필드여야 합니다 (분 시 일 월 요일), %d개를 받았습니다", len(fields))
	}
	minutes, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("분 필드: %w", err)
	}
	hours, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("시 필드: %w", err)
	}
	days, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("일 필드: %w", err)
	}
	months, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("월 필드: %w", err)
	}
	weekdays, err := parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("요일 필드: %w", err)
	}
	return &Schedule{
		minutes: minutes, hours: hours, days: days, months: months, weekdays: weekdays,
		daysStar: fields[2] == "*", weekdayAny: fields[4] == "*",
	}, nil
}

// Next returns the first matching time strictly after `after`, in the given
// location. It returns the zero time if nothing matches within four years, which
// only happens for an impossible date such as 30 February.
func (s *Schedule) Next(after time.Time, location *time.Location) time.Time {
	if location == nil {
		location = time.UTC
	}
	// Start at the next whole minute; cron has minute resolution.
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(4, 0, 0)
	for candidate.Before(limit) {
		if !s.months[int(candidate.Month())] {
			// Skip to the first day of the next month rather than stepping a minute
			// at a time through a month that can never match.
			candidate = time.Date(candidate.Year(), candidate.Month(), 1, 0, 0, 0, 0, location).AddDate(0, 1, 0)
			continue
		}
		if !s.matchesDay(candidate) {
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
			continue
		}
		if !s.hours[candidate.Hour()] {
			candidate = candidate.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		if !s.minutes[candidate.Minute()] {
			candidate = candidate.Add(time.Minute)
			continue
		}
		return candidate
	}
	return time.Time{}
}

// matchesDay applies the traditional cron rule: when both day-of-month and
// day-of-week are restricted, either matching is enough.
func (s *Schedule) matchesDay(candidate time.Time) bool {
	dayMatch := s.days[candidate.Day()]
	weekdayMatch := s.weekdays[int(candidate.Weekday())]
	switch {
	case s.daysStar && s.weekdayAny:
		return true
	case s.daysStar:
		return weekdayMatch
	case s.weekdayAny:
		return dayMatch
	default:
		return dayMatch || weekdayMatch
	}
}

func parseField(field string, min, max int) (map[int]bool, error) {
	values := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("빈 값이 있습니다")
		}
		spec, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			parsed, err := strconv.Atoi(stepText)
			if err != nil || parsed < 1 {
				return nil, fmt.Errorf("간격 %q이 올바르지 않습니다", stepText)
			}
			step = parsed
		}
		low, high := min, max
		if spec != "*" {
			startText, endText, isRange := strings.Cut(spec, "-")
			start, err := strconv.Atoi(startText)
			if err != nil {
				return nil, fmt.Errorf("숫자가 아닌 값 %q", startText)
			}
			low = start
			high = start
			if isRange {
				end, err := strconv.Atoi(endText)
				if err != nil {
					return nil, fmt.Errorf("숫자가 아닌 값 %q", endText)
				}
				high = end
			} else if hasStep {
				// `5/15` means "from 5 to the end of the range, every 15".
				high = max
			}
		}
		if low < min || high > max || low > high {
			return nil, fmt.Errorf("%d~%d 범위를 벗어났습니다", min, max)
		}
		for value := low; value <= high; value += step {
			values[value] = true
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("일치하는 값이 없습니다")
	}
	return values, nil
}

// NextFireAt computes a trigger's next run, resolving its timezone. An unknown
// timezone falls back to UTC rather than silently never firing.
func NextFireAt(schedule, timezone string, after time.Time) (time.Time, error) {
	parsed, err := ParseSchedule(schedule)
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	next := parsed.Next(after, location)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("이 일정으로 실행될 시각을 찾지 못했습니다")
	}
	return next.UTC(), nil
}
