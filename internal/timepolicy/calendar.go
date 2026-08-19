package timepolicy

import (
	"fmt"
	"sort"
	"time"

	"go-base/internal/domain"
)

type BusinessDay struct {
	Date     time.Time
	Open     bool
	OpensAt  time.Duration
	ClosesAt time.Duration
	Reason   string
}

type Calendar struct {
	Location *time.Location
	Days     map[string]BusinessDay
	Weekdays map[time.Weekday]BusinessDay
}

type DeadlineRule struct {
	Name            string
	Duration        time.Duration
	BusinessTime    bool
	Cutoff          time.Duration
	RollAfterCutoff bool
}

type Period struct {
	StartsAt time.Time
	EndsAt   time.Time
}

func (calendar Calendar) Validate() error {
	if calendar.Location == nil {
		return fmt.Errorf("%w: calendar location", domain.ErrInvalid)
	}
	for weekday, day := range calendar.Weekdays {
		if weekday < time.Sunday || weekday > time.Saturday {
			return fmt.Errorf("%w: calendar weekday", domain.ErrInvalid)
		}
		if err := validateDay(day); err != nil {
			return err
		}
	}
	for key, day := range calendar.Days {
		if _, err := time.ParseInLocation("2006-01-02", key, calendar.Location); err != nil {
			return fmt.Errorf("%w: calendar date %s", domain.ErrInvalid, key)
		}
		if err := validateDay(day); err != nil {
			return err
		}
	}
	return nil
}

func validateDay(day BusinessDay) error {
	if !day.Open {
		return nil
	}
	if day.OpensAt < 0 || day.ClosesAt <= day.OpensAt || day.ClosesAt > 24*time.Hour {
		return fmt.Errorf("%w: business day hours", domain.ErrInvalid)
	}
	return nil
}

func (calendar Calendar) Day(at time.Time) (BusinessDay, error) {
	if err := calendar.Validate(); err != nil {
		return BusinessDay{}, err
	}
	local := at.In(calendar.Location)
	key := local.Format("2006-01-02")
	if day, exists := calendar.Days[key]; exists {
		day.Date = dateOnly(local)
		return day, nil
	}
	day, exists := calendar.Weekdays[local.Weekday()]
	if !exists {
		day = BusinessDay{Open: false, Reason: "weekday is closed"}
	}
	day.Date = dateOnly(local)
	return day, nil
}

func (calendar Calendar) NextOpen(at time.Time) (time.Time, error) {
	local := at.In(calendar.Location)
	for index := 0; index < 370; index++ {
		day, err := calendar.Day(local)
		if err != nil {
			return time.Time{}, err
		}
		if day.Open {
			open := day.Date.Add(day.OpensAt)
			closeAt := day.Date.Add(day.ClosesAt)
			if local.Before(open) {
				return open, nil
			}
			if local.Before(closeAt) {
				return local, nil
			}
		}
		local = dateOnly(local).AddDate(0, 0, 1)
	}
	return time.Time{}, fmt.Errorf("%w: no open day in calendar horizon", domain.ErrNotFound)
}

func (calendar Calendar) AddBusinessDuration(start time.Time, duration time.Duration) (time.Time, error) {
	if duration < 0 {
		return time.Time{}, fmt.Errorf("%w: negative business duration", domain.ErrInvalid)
	}
	current, err := calendar.NextOpen(start)
	if err != nil {
		return time.Time{}, err
	}
	remaining := duration
	for steps := 0; steps < 10000; steps++ {
		if remaining == 0 {
			return current, nil
		}
		day, err := calendar.Day(current)
		if err != nil {
			return time.Time{}, err
		}
		closeAt := day.Date.Add(day.ClosesAt)
		available := closeAt.Sub(current)
		if remaining <= available {
			return current.Add(remaining), nil
		}
		remaining -= available
		current, err = calendar.NextOpen(day.Date.AddDate(0, 0, 1))
		if err != nil {
			return time.Time{}, err
		}
	}
	return time.Time{}, fmt.Errorf("%w: business duration exceeded safety horizon", domain.ErrConflict)
}

func (calendar Calendar) Deadline(start time.Time, rule DeadlineRule) (time.Time, error) {
	if rule.Name == "" || rule.Duration < 0 || rule.Cutoff < 0 || rule.Cutoff > 24*time.Hour {
		return time.Time{}, fmt.Errorf("%w: deadline rule", domain.ErrInvalid)
	}
	local := start.In(calendar.Location)
	if rule.RollAfterCutoff && clockOfDay(local) >= rule.Cutoff {
		next, err := calendar.NextOpen(dateOnly(local).AddDate(0, 0, 1))
		if err != nil {
			return time.Time{}, err
		}
		local = next
	}
	if rule.BusinessTime {
		return calendar.AddBusinessDuration(local, rule.Duration)
	}
	return local.Add(rule.Duration), nil
}

func SplitByBusinessDay(calendar Calendar, period Period) ([]Period, error) {
	if !period.EndsAt.After(period.StartsAt) {
		return nil, fmt.Errorf("%w: period", domain.ErrInvalid)
	}
	current := period.StartsAt.In(calendar.Location)
	end := period.EndsAt.In(calendar.Location)
	result := []Period{}
	for current.Before(end) {
		day, err := calendar.Day(current)
		if err != nil {
			return nil, err
		}
		dayEnd := day.Date.AddDate(0, 0, 1)
		segmentEnd := dayEnd
		if end.Before(segmentEnd) {
			segmentEnd = end
		}
		if day.Open {
			open := day.Date.Add(day.OpensAt)
			closeAt := day.Date.Add(day.ClosesAt)
			segmentStart := current
			if segmentStart.Before(open) {
				segmentStart = open
			}
			if segmentEnd.After(closeAt) {
				segmentEnd = closeAt
			}
			if segmentEnd.After(segmentStart) {
				result = append(result, Period{StartsAt: segmentStart, EndsAt: segmentEnd})
			}
		}
		current = dayEnd
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartsAt.Before(result[j].StartsAt) })
	return result, nil
}

func BusinessDuration(calendar Calendar, period Period) (time.Duration, error) {
	segments, err := SplitByBusinessDay(calendar, period)
	if err != nil {
		return 0, err
	}
	var total time.Duration
	for _, segment := range segments {
		total += segment.EndsAt.Sub(segment.StartsAt)
	}
	return total, nil
}

func dateOnly(at time.Time) time.Time {
	year, month, day := at.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, at.Location())
}

func clockOfDay(at time.Time) time.Duration {
	return time.Duration(at.Hour())*time.Hour + time.Duration(at.Minute())*time.Minute + time.Duration(at.Second())*time.Second + time.Duration(at.Nanosecond())
}
