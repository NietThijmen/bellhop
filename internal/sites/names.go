package sites

import (
	"regexp"
	"strings"
	"unicode"
)

// maxLabel is the longest DNS label.
const maxLabel = 63

// maxHostname is the longest hostname in presentation form.
const maxHostname = 253

// translit maps common lower-case Latin letters with diacritics (and a few
// ligatures) to ASCII. Anything not listed and not ASCII is dropped.
var translit = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'æ': "ae",
	'ç': "c", 'ć': "c", 'č': "c", 'ĉ': "c", 'ċ': "c",
	'ď': "d", 'đ': "d", 'ð': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ĕ': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ğ': "g", 'ĝ': "g", 'ġ': "g", 'ģ': "g",
	'ĥ': "h", 'ħ': "h",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'ĭ': "i", 'į': "i", 'ı': "i",
	'ĵ': "j",
	'ķ': "k",
	'ĺ': "l", 'ļ': "l", 'ľ': "l", 'ŀ': "l", 'ł': "l",
	'ñ': "n", 'ń': "n", 'ņ': "n", 'ň': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ŏ': "o", 'ő': "o",
	'œ': "oe",
	'ŕ': "r", 'ŗ': "r", 'ř': "r",
	'ś': "s", 'ŝ': "s", 'ş': "s", 'š': "s", 'ș': "s", 'ß': "ss",
	'ţ': "t", 'ť': "t", 'ŧ': "t", 'ț': "t", 'þ': "th",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ũ': "u", 'ū': "u", 'ŭ': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ŵ': "w",
	'ý': "y", 'ÿ': "y", 'ŷ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
}

// SanitizeName turns a directory name into a site name: a single DNS label of
// [a-z0-9-], at most 63 characters, without leading, trailing or repeated
// dashes. Spaces, underscores and dots become dashes; common accented Latin
// letters are transliterated (é -> e); anything else is dropped. It returns ""
// when nothing usable is left.
func SanitizeName(dir string) string {
	var b strings.Builder
	dash := false // last written rune was a dash
	for _, r := range dir {
		r = unicode.ToLower(r)
		var out string
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = string(r)
		case r == '-', r == ' ', r == '_', r == '.':
			out = "-"
		default:
			out = translit[r]
		}
		if out == "" {
			continue
		}
		if out == "-" {
			if dash || b.Len() == 0 {
				continue
			}
			dash = true
		} else {
			dash = false
		}
		b.WriteString(out)
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > maxLabel {
		s = s[:maxLabel]
		// Prefer cutting at a word boundary when that keeps most of the name.
		if i := strings.LastIndexByte(s, '-'); i >= maxLabel/2 {
			s = s[:i]
		}
		s = strings.TrimRight(s, "-")
	}
	return s
}

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validLabel reports whether s is a lower-case DNS label.
func validLabel(s string) bool {
	return len(s) <= maxLabel && labelRE.MatchString(s)
}

// validHostname reports whether s is a lower-case hostname of one or more
// valid labels.
func validHostname(s string) bool {
	if s == "" || len(s) > maxHostname {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !validLabel(l) {
			return false
		}
	}
	return true
}

var phpVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// validPHPVersion reports whether s looks like a major.minor PHP version.
func validPHPVersion(s string) bool {
	return phpVersionRE.MatchString(s)
}
