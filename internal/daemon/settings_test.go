package daemon

import (
	"errors"
	"testing"

	"github.com/nietthijmen/bellhop/internal/api"
)

func TestValidateSettings(t *testing.T) {
	tests := []struct {
		name  string
		patch api.Settings
		want  api.Settings
		bad   bool
	}{
		{name: "tld lowercased", patch: api.Settings{"tld": " .Test "}, want: api.Settings{"tld": "test"}},
		{name: "tld with dash", patch: api.Settings{"tld": "my-dev"}, want: api.Settings{"tld": "my-dev"}},
		{name: "tld empty", patch: api.Settings{"tld": ""}, bad: true},
		{name: "tld dot inside", patch: api.Settings{"tld": "a.b"}, bad: true},
		{name: "tld leading dash", patch: api.Settings{"tld": "-x"}, bad: true},
		{name: "tld numeric", patch: api.Settings{"tld": "123"}, bad: true},
		{name: "tld too long", patch: api.Settings{"tld": "a234567890123456789012345678901234567890123456789012345678901234"}, bad: true},
		{name: "bool yes", patch: api.Settings{"localhost_mode": "Yes"}, want: api.Settings{"localhost_mode": "true"}},
		{name: "bool 0", patch: api.Settings{"localhost_mode": "0"}, want: api.Settings{"localhost_mode": "false"}},
		{name: "bool junk", patch: api.Settings{"localhost_mode": "maybe"}, bad: true},
		{name: "php version", patch: api.Settings{"default_php": "8.3"}, want: api.Settings{"default_php": "8.3"}},
		{name: "php clear", patch: api.Settings{"default_php": ""}, want: api.Settings{"default_php": ""}},
		{name: "php patch level", patch: api.Settings{"default_php": "8.3.1"}, bad: true},
		{name: "unknown key", patch: api.Settings{"tld": "test", "nope": "1"}, bad: true},
		{name: "empty patch", patch: api.Settings{}, bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateSettings(tt.patch)
			if tt.bad {
				var ae *api.Error
				if !errors.As(err, &ae) || ae.Code != api.CodeBadRequest || ae.Hint == "" {
					t.Fatalf("err = %v, want bad_request api.Error with a hint", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestDefaultsAreValid(t *testing.T) {
	for k, v := range DefaultSettings() {
		if _, err := validateSettings(api.Settings{k: v}); err != nil {
			t.Errorf("default %s=%q is rejected: %v", k, v, err)
		}
	}
}
