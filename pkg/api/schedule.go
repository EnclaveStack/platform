package api

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // time zones resolve the same on every host (ADR-017)

	"github.com/robfig/cron/v3"
)

// Descriptors are the @-schedules allowed besides 5-field cron (L3-002 §6.5).
var Descriptors = []string{"@hourly", "@daily", "@weekly", "@monthly"}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Schedule is a parsed cron schedule in a time zone.
type Schedule struct {
	sched cron.Schedule
	loc   *time.Location
}

// ParseSchedule parses a 5-field cron expression or one of Descriptors, evaluated in tz (IANA; ""
// means the machine's local zone).
func ParseSchedule(expr, tz string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "@") {
		ok := false
		for _, d := range Descriptors {
			ok = ok || expr == d
		}
		if !ok {
			return Schedule{}, fmt.Errorf("use one of %s, or a 5-field cron expression", strings.Join(Descriptors, ", "))
		}
	}
	loc := time.Local
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return Schedule{}, fmt.Errorf("unknown time zone %q", tz)
		}
		loc = l
	}
	s, err := cronParser.Parse(expr)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{sched: s, loc: loc}, nil
}

// Next returns the first occurrence strictly after t, in UTC.
func (s Schedule) Next(t time.Time) time.Time {
	return s.sched.Next(t.In(s.loc)).UTC()
}
