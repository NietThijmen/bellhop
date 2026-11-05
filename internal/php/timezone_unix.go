//go:build unix

package php

import "os"

var (
	localtimePath   = "/etc/localtime"
	etcTimezonePath = "/etc/timezone"
)

func systemTimezone() string {
	if tz, ok := timezoneFromEnv(); ok {
		return tz
	}
	if p, err := os.Readlink(localtimePath); err == nil {
		if tz, ok := normalizeTimezone(p); ok {
			return tz
		}
	}
	if b, err := os.ReadFile(etcTimezonePath); err == nil {
		if tz, ok := normalizeTimezone(string(b)); ok {
			return tz
		}
	}
	return "UTC"
}
