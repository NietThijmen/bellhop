package daemon

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nietthijmen/bellhop/internal/api"
)

// Setting keys known to the daemon. Only these may be read or written over
// the API; anything else in the store is ignored.
const (
	SettingTLD           = "tld"
	SettingLocalhostMode = "localhost_mode"
	SettingDefaultPHP    = "default_php"
)

type settingDef struct {
	def       string
	normalize func(string) (string, error) // returns the value to store
}

var settingDefs = map[string]settingDef{
	SettingTLD:           {def: "test", normalize: normalizeTLD},
	SettingLocalhostMode: {def: "false", normalize: normalizeBool},
	SettingDefaultPHP:    {def: "", normalize: normalizePHPVersion},
}

// SettingKeys returns the known setting keys, sorted.
func SettingKeys() []string {
	keys := make([]string, 0, len(settingDefs))
	for k := range settingDefs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// DefaultSettings returns every known setting at its default value.
func DefaultSettings() api.Settings {
	out := api.Settings{}
	for k, d := range settingDefs {
		out[k] = d.def
	}
	return out
}

var (
	tldRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	phpRe = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
)

func normalizeTLD(v string) (string, error) {
	v = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "."))
	if !tldRe.MatchString(v) {
		return "", fmt.Errorf("%q is not a valid top-level domain: use 1-63 letters, digits or '-', not starting or ending with '-' (for example \"test\")", v)
	}
	if _, err := strconv.Atoi(v); err == nil {
		return "", fmt.Errorf("%q is not a valid top-level domain: it must contain a letter", v)
	}
	return v, nil
}

func normalizeBool(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return "true", nil
	case "0", "false", "no", "off":
		return "false", nil
	}
	return "", fmt.Errorf("%q is not a boolean: use true or false", v)
}

func normalizePHPVersion(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || phpRe.MatchString(v) {
		return v, nil
	}
	return "", fmt.Errorf("%q is not a PHP version: use major.minor such as \"8.3\", or an empty value to clear it", v)
}

// validateSettings checks and normalises a patch. Every key must be known and
// every value valid; otherwise nothing is applied and an *api.Error says why.
func validateSettings(patch api.Settings) (api.Settings, error) {
	if len(patch) == 0 {
		return nil, &api.Error{
			Code:    api.CodeBadRequest,
			Message: "no settings given",
			Hint:    "send at least one of: " + strings.Join(SettingKeys(), ", "),
		}
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	slices.Sort(keys) // deterministic error for multiple bad keys
	out := make(api.Settings, len(patch))
	for _, k := range keys {
		d, ok := settingDefs[k]
		if !ok {
			return nil, &api.Error{
				Code:    api.CodeBadRequest,
				Message: fmt.Sprintf("unknown setting %q", k),
				Hint:    "known settings: " + strings.Join(SettingKeys(), ", "),
			}
		}
		v, err := d.normalize(patch[k])
		if err != nil {
			return nil, &api.Error{
				Code:    api.CodeBadRequest,
				Message: fmt.Sprintf("invalid value for setting %q", k),
				Hint:    err.Error(),
			}
		}
		out[k] = v
	}
	return out, nil
}
