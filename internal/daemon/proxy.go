package daemon

import (
	"context"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/proxy"
)

// proxyReloaded is the Data of api.EventProxyReloaded.
type proxyReloaded struct {
	Port int `json:"port"`
}

// proxyError is the Data of api.EventProxyError.
type proxyError struct {
	Message string `json:"message"`
}

// reloadProxy rebuilds the Caddy config from the current site list.
// OnError publishes proxy.error, so a failed Reload is only logged here.
func (d *daemon) reloadProxy() {
	if d.proxy == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sites, err := d.proxySites(ctx)
	if err != nil {
		d.log.Error("list sites for proxy", "err", err)
		d.bus.Publish(api.EventProxyError, proxyError{Message: err.Error()})
		return
	}
	if err := d.proxy.Reload(ctx, sites); err != nil {
		d.log.Error("proxy reload", "err", err)
	}
}

func (d *daemon) proxySites(ctx context.Context) ([]proxy.Site, error) {
	list, _, err := d.sites.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]proxy.Site, 0, len(list))
	for _, s := range list {
		item := proxy.Site{
			Name:    s.Name,
			Hosts:   s.Hosts,
			Plan:    s.Plan,
			PlanErr: s.PlanErr,
		}
		if s.Plan.PHP && s.PlanErr == nil {
			ver := s.PHP
			if ver == "" {
				ver = php.DefaultVersion
			}
			dials, err := php.PoolDials(d.dirs.Run, ver)
			if err != nil {
				item.PlanErr = err
			} else {
				item.Sockets = dials
			}
		}
		out = append(out, item)
	}
	return out, nil
}
