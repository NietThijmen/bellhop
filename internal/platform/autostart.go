package platform

import "github.com/nietthijmen/bellhop/pkg/version"

// AutostartTask is the logon task, user unit, or LaunchAgent label.
func AutostartTask() string { return version.ID }

// SchtasksCreateArgs schedules binary at logon for the current user.
func SchtasksCreateArgs(task, binary string) []string {
	return []string{"/Create", "/F", "/SC", "ONLOGON", "/TN", task, "/TR", binary, "/RL", "LIMITED"}
}

// SchtasksDeleteArgs removes the logon task.
func SchtasksDeleteArgs(task string) []string {
	return []string{"/Delete", "/F", "/TN", task}
}

// SystemdUserUnit is a user service that starts the daemon at login.
func SystemdUserUnit(binary string) string {
	return "[Unit]\nDescription=Bellhop\n\n[Service]\nExecStart=" + binary + "\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n"
}

// LaunchAgentPlist is a per-user LaunchAgent.
func LaunchAgentPlist(label, binary string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + label + `</string>
<key>ProgramArguments</key><array><string>` + binary + `</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
`
}
