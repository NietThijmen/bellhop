//go:build windows

package platform

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// VCRedist is the Visual C++ redistributable a Windows PHP build needs.
type VCRedist int

const (
	// VCRedist2019 is the VS 2019 runtime (vcruntime140.dll 14.20 or newer).
	// PHP 8.1–8.3 Windows builds (vs16) need it. A newer redist satisfies it.
	VCRedist2019 VCRedist = iota
	// VCRedist2022 is the VS 2022 runtime (vcruntime140.dll 14.30 or newer).
	// PHP 8.4 and later (vs17) need it.
	VCRedist2022
)

// ErrVCRedistMissing means vcruntime140.dll is absent or older than the
// PHP line requires. The error text points at Microsoft's installer.
// Bellhop does not download or install the redistributable.
var ErrVCRedistMissing = errors.New("the Microsoft Visual C++ redistributable is not installed")

const vcRuntimeDLL = "vcruntime140.dll"

// CheckVCRuntime reports whether path is a vcruntime140.dll new enough for
// need. An empty path checks the System32 copy. The error wraps
// ErrVCRedistMissing and names the redistributable to install.
func CheckVCRuntime(path string, need VCRedist) error {
	if path == "" {
		dir, err := windows.GetSystemDirectory()
		if err != nil {
			return fmt.Errorf("%w: %s", ErrVCRedistMissing, redistHint(need))
		}
		path = dir + `\` + vcRuntimeDLL
	}
	major, minor, err := fileVersion(path)
	if err != nil {
		return fmt.Errorf("%w (%w): %s", ErrVCRedistMissing, err, redistHint(need))
	}
	if !versionOK(major, minor, need) {
		return fmt.Errorf("%w: found %s %d.%d, need %s", ErrVCRedistMissing, vcRuntimeDLL, major, minor, redistHint(need))
	}
	return nil
}

func redistHint(need VCRedist) string {
	switch need {
	case VCRedist2022:
		return "the Visual C++ Redistributable for Visual Studio 2022 (vcruntime140.dll 14.30 or newer) from https://aka.ms/vs/17/release/vc_redist.x64.exe"
	case VCRedist2019:
		return "the Visual C++ Redistributable for Visual Studio 2019 (vcruntime140.dll 14.20 or newer) from https://aka.ms/vs/16/release/vc_redist.x64.exe"
	default:
		return "the Visual C++ Redistributable from https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist"
	}
}

func versionOK(major, minor int, need VCRedist) bool {
	if major > 14 {
		return true
	}
	if major < 14 {
		return false
	}
	switch need {
	case VCRedist2019:
		return minor >= 20
	case VCRedist2022:
		return minor >= 30
	default:
		return false
	}
}

func fileVersion(path string) (major, minor int, err error) {
	var zero windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &zero)
	if err != nil {
		return 0, 0, err
	}
	if size == 0 {
		return 0, 0, fmt.Errorf("%s has no version resource", path)
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil { //nolint:gosec // G103: Win32 API buffer
		return 0, 0, err
	}
	var info *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&info), &n); err != nil { //nolint:gosec // G103: Win32 API buffer
		return 0, 0, err
	}
	if info == nil || n < uint32(unsafe.Sizeof(*info)) {
		return 0, 0, fmt.Errorf("%s has no fixed version info", path)
	}
	// VS_FFI_SIGNATURE.
	if info.Signature != 0xFEEF04BD {
		return 0, 0, fmt.Errorf("%s version resource is not a fixed file info", path)
	}
	return int(info.FileVersionMS >> 16), int(info.FileVersionMS & 0xffff), nil
}
