package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/api"
)

func TestCertsCommands(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	ctx := testCtx(t)

	shown := run(ctx, addr, "certs")
	if shown.code != ExitOK {
		t.Fatalf("certs exit %d stderr %q", shown.code, shown.stderr)
	}
	if !strings.Contains(shown.stdout, "bellhop") || !strings.Contains(shown.stdout, "abc") || !strings.Contains(shown.stdout, "no") {
		t.Fatalf("certs output:\n%s", shown.stdout)
	}

	exported := run(ctx, addr, "certs", "export", "--json")
	if exported.code != ExitOK {
		t.Fatalf("export exit %d stderr %q", exported.code, exported.stderr)
	}
	var out api.CertExport
	if err := json.Unmarshal([]byte(exported.stdout), &out); err != nil {
		t.Fatalf("export json: %v\n%s", err, exported.stdout)
	}
	if !strings.Contains(out.PEM, "BEGIN CERTIFICATE") || out.Written != "" {
		t.Fatalf("export = %+v", out)
	}
}
