//go:build windows || darwin

package platform

import "strings"

const caseInsensitivePaths = true

// foldPath lower-cases p. Simple lower-casing matches NTFS and APFS for every
// name a project folder realistically has; neither filesystem applies full
// Unicode case folding either.
func foldPath(p string) string {
	return strings.ToLower(p)
}
