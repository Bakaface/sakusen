package config

import (
	"time"

	cron "github.com/robfig/cron/v3"
)

// periodicCronParser parses periodic cadence specs. It uses the standard
// 5-field cron parser (minute granularity) plus descriptors (@hourly, @daily,
// @weekly, ...) and the @every shorthand.
//
// Granularity note: 5-field cron expressions are minute-granular (there is no
// seconds field), so the finest cron cadence is once per minute. The @every
// shorthand, however, accepts any Go duration — including sub-minute intervals
// like "@every 30s". The scheduler polls on a 30s ticker, so sub-minute @every
// cadences are observed at roughly tick resolution rather than to the second
// (a validation warning surfaces this).
var periodicCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ParsePeriodicCadence parses a cadence string and returns its schedule.
// Accepts standard 5-field cron, descriptors (@daily, @hourly, ...), and the
// @every <duration> shorthand. Returns an error for unparseable specs.
func ParsePeriodicCadence(cadence string) (cron.Schedule, error) {
	return periodicCronParser.Parse(cadence)
}

// NextPeriodicFire returns the next fire time strictly after `from` for the
// given cadence. Returns an error if the cadence does not parse.
func NextPeriodicFire(cadence string, from time.Time) (time.Time, error) {
	sched, err := ParsePeriodicCadence(cadence)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}
