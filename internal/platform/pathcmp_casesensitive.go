//go:build !windows && !darwin

package platform

const caseInsensitivePaths = false

func foldPath(p string) string {
	return p
}
