package daemon

import (
	"testing"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/php"
)

func TestPHPCatalog(t *testing.T) {
	home, addr := testEnv(t)
	d := start(t, home, addr, []ServiceProvider{})
	ctx := testCtx(t)

	versions, err := d.client.PHPVersions(ctx)
	if err != nil {
		t.Fatalf("PHPVersions: %v", err)
	}
	var found bool
	for _, v := range versions {
		if v.Version == php.DefaultVersion {
			found = true
			if v.Installed {
				t.Fatalf("PHP %s Installed = true, want false in a fresh data dir", v.Version)
			}
			if v.Patch == "" {
				t.Fatalf("PHP %s has no patch in the catalog", v.Version)
			}
		}
	}
	if !found {
		t.Fatalf("catalog = %+v, want a %s line", versions, php.DefaultVersion)
	}

	_, err = d.client.InstallPHP(ctx, "not-a-version")
	apiErr(t, err, api.CodeBadRequest)
	ref, err := d.client.InstallPHP(ctx, "9.9")
	if err == nil {
		t.Fatalf("install 9.9 started job %s, want not_found", ref.JobID)
	}
	apiErr(t, err, api.CodeNotFound)
}
