package platform

import "path/filepath"

// Path comparison.
//
// Bellhop compares paths the way the default filesystem of each OS does:
// case-insensitively on Windows (NTFS) and macOS, case-sensitively elsewhere.
// On macOS the default APFS volume format is case-insensitive (but
// case-preserving); a volume deliberately formatted as case-sensitive APFS
// would be treated as case-insensitive here too, which at worst refuses to
// park two folders whose paths differ only in case. Paths are always stored
// and displayed with their original case; PathKey is only for comparing.

// PathKey returns a comparison key for p: cleaned, made absolute, and on
// case-insensitive platforms case-folded. Two paths that name the same
// location on the default filesystem have the same key. It does not touch the
// filesystem and does not resolve symlinks.
func PathKey(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	} else {
		p = filepath.Clean(p)
	}
	return foldPath(p)
}

// SamePath reports whether a and b name the same location according to
// PathKey.
func SamePath(a, b string) bool {
	return PathKey(a) == PathKey(b)
}

// CaseInsensitivePaths reports whether this platform compares paths
// case-insensitively.
func CaseInsensitivePaths() bool {
	return caseInsensitivePaths
}
