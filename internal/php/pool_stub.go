//go:build !unix

package php

import (
	"fmt"

	"github.com/nietthijmen/bellhop/internal/supervisor"
)

const poolsSupported = false

// PoolSpec reports that this operating system has no php-fpm pool in this
// milestone. Windows installs the PHP zip and its ini files through Install;
// worker processes are added later.
func (m *Manager) PoolSpec(version string) (supervisor.Spec, error) {
	if version == "" {
		version = DefaultVersion
	}
	return supervisor.Spec{}, fmt.Errorf("PHP %s pool: %w; Windows php-cgi pools are a later milestone", version, ErrNoPool)
}
