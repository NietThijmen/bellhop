package php

import (
	"os"
	"strings"
)

func timezoneFromEnv() (string, bool) {
	tz, ok := normalizeTimezone(os.Getenv("TZ"))
	if !ok || tz == "local" {
		return "", false
	}
	return tz, true
}

// normalizeTimezone accepts an IANA name or a zoneinfo path
// ("Europe/Amsterdam", ":Europe/Amsterdam", "/usr/share/zoneinfo/Europe/Amsterdam").
func normalizeTimezone(raw string) (string, bool) {
	tz := strings.TrimSpace(raw)
	tz = strings.TrimPrefix(tz, ":")
	const marker = "zoneinfo/"
	if i := strings.LastIndex(tz, marker); i >= 0 {
		tz = tz[i+len(marker):]
	}
	tz = strings.Trim(tz, `/\`)
	if tz == "" || tz == "local" || strings.Contains(tz, "..") || strings.ContainsAny(tz, "\r\n;\"") {
		return "", false
	}
	return tz, true
}

func cleanTimezone(tz string) string {
	norm, ok := normalizeTimezone(tz)
	if !ok {
		return "UTC"
	}
	return norm
}
