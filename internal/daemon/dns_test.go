package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/dns"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/store"
)

func TestDNSProvider(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d := &daemon{store: st, log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()

	svcs, err := d.dnsProvider().Services(ctx, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || !svcs[0].Autostart || svcs[0].Spec.Name != dns.ServiceName {
		t.Fatalf("services = %+v", svcs)
	}
	spec := svcs[0].Spec
	if spec.Ready.TCPAddr != platform.DNSListenAddr() || spec.Restart != "on-failure" {
		t.Fatalf("spec = %+v", spec)
	}
	joined := strings.Join(spec.Env, "\n")
	for _, want := range []string{dns.EnvActive + "=1", dns.EnvTLD + "=test", dns.EnvAddr + "=" + platform.DNSListenAddr()} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q\n%s", want, joined)
		}
	}
	if err := st.SetSetting(ctx, SettingLocalhostMode, "true"); err != nil {
		t.Fatal(err)
	}
	svcs, err = d.dnsProvider().Services(ctx, Env{})
	if err != nil || len(svcs) != 0 {
		t.Fatalf("localhost mode services = %+v, %v", svcs, err)
	}
}
