package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nietthijmen/bellhop/internal/dns"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

// dnsProvider supervises the local resolver unless localhost mode is on.
// The process is this executable with BELLHOP_DNS set, so it serves DNS
// instead of becoming another daemon.
func (d *daemon) dnsProvider() ServiceProvider {
	return ServiceProvider{
		Name: dns.ServiceName,
		Services: func(ctx context.Context, _ Env) ([]Service, error) {
			s, err := d.settings(ctx)
			if err != nil {
				return nil, err
			}
			if s[SettingLocalhostMode] == "true" {
				return nil, nil
			}
			spec, err := dnsSpec(s[SettingTLD])
			if err != nil {
				return nil, err
			}
			return []Service{{Spec: spec, Autostart: true}}, nil
		},
	}
}

func dnsSpec(tld string) (supervisor.Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return supervisor.Spec{}, fmt.Errorf("find the daemon executable: %w", err)
	}
	addr := platform.DNSListenAddr()
	return supervisor.Spec{
		Name:   dns.ServiceName,
		Binary: exe,
		Env: []string{
			dns.EnvActive + "=1",
			dns.EnvTLD + "=" + tld,
			dns.EnvAddr + "=" + addr,
		},
		Ready:   supervisor.Readiness{TCPAddr: addr},
		Restart: supervisor.RestartOnFailure,
	}, nil
}

// syncDNS starts, restarts or stops the resolver after tld or localhost
// mode changes. Daemons that did not register the built-in provider skip it.
func (d *daemon) syncDNS(ctx context.Context) {
	if !d.manageDNS || d.sup == nil {
		return
	}
	s, err := d.settings(ctx)
	if err != nil {
		d.log.Error("read settings for dns", "err", err)
		return
	}
	_, exists := d.sup.Status(dns.ServiceName)
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if s[SettingLocalhostMode] == "true" {
		if !exists {
			return
		}
		if err := d.sup.Stop(stopCtx, dns.ServiceName); err != nil {
			d.log.Error("stop dns", "err", err)
		}
		if err := d.sup.Remove(stopCtx, dns.ServiceName); err != nil {
			d.log.Error("remove dns", "err", err)
		}
		return
	}
	spec, err := dnsSpec(s[SettingTLD])
	if err != nil {
		d.log.Error("prepare dns", "err", err)
		return
	}
	if exists {
		if err := d.sup.Stop(stopCtx, dns.ServiceName); err != nil {
			d.log.Error("stop dns", "err", err)
			return
		}
	}
	if err := d.sup.Add(spec); err != nil {
		d.log.Error("register dns", "err", err)
		return
	}
	if !exists {
		d.mu.Lock()
		d.stopBudget += specStopBudget(spec)
		d.mu.Unlock()
	}
	if err := d.sup.Start(stopCtx, dns.ServiceName); err != nil {
		d.log.Error("start dns", "err", err)
	}
}
