//go:build windows

package php

import "testing"

func TestWindowsTimezoneNames(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "W. Europe Standard Time", want: "Europe/Berlin"},
		{in: "Pacific Standard Time", want: "America/Los_Angeles"},
		{in: "UTC", want: "UTC"},
		{in: "New Zealand Standard Time", want: "Pacific/Auckland"},
	}
	for _, tc := range cases {
		got, ok := ianaFromWindows(tc.in)
		if !ok || got != tc.want {
			t.Errorf("ianaFromWindows(%q) = %q, %v", tc.in, got, ok)
		}
	}
	if _, ok := ianaFromWindows("Not A Real Zone"); ok {
		t.Fatal("unknown windows zone was accepted")
	}
}
