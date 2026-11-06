//go:build windows

package platform

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

func openURL(u string) error    { return shellOpen(u) }
func openPath(dir string) error { return shellOpen(dir) }

// shellOpen asks the shell to "open" target (the default browser for a URL,
// Explorer for a folder) via ShellExecuteW. No command line is built, so the
// target is never interpreted by cmd.exe.
func shellOpen(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("invalid target %q: %w", target, err)
	}
	// ShellExecute may delegate to COM-based handlers; Microsoft requires
	// COM to be initialised (apartment-threaded) on the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("ShellExecute: %w", err)
	}
	return nil
}
