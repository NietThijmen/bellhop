// Package manifests embeds the release manifests shipped inside the binary.
package manifests

import _ "embed"

// Artifacts is the embedded artifacts.json: download URLs and SHA-256 sums
// for every installable component per OS/arch/version. It is parsed by
// internal/artifacts.
//
//go:embed artifacts.json
var Artifacts []byte
