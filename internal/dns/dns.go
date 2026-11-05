// Package dns resolves one configured TLD to the loopback addresses and
// refuses every other name. It is the local resolver the helper points the
// operating system at. Localhost mode does not start it.
package dns

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
	"github.com/nietthijmen/bellhop/internal/platform"
)

// ServiceName is the supervisor service that runs this resolver.
const ServiceName = "dns"

// EnvActive, EnvTLD and EnvAddr are set on that process.
const (
	EnvActive = "BELLHOP_DNS"
	EnvTLD    = "BELLHOP_DNS_TLD"
	EnvAddr   = "BELLHOP_DNS_ADDR"
)

// TTL is the time to live of every answer.
const TTL uint32 = 60

// Resolver answers DNS queries for one TLD.
type Resolver struct {
	TLD string
}

// Answer builds the response for req. Names equal to the TLD or below it
// get A 127.0.0.1 and AAAA ::1. Every other question is REFUSED.
func (r Resolver) Answer(req *mdns.Msg) *mdns.Msg {
	out := new(mdns.Msg)
	if req == nil || len(req.Question) == 0 || req.Opcode != mdns.OpcodeQuery {
		if req == nil {
			req = new(mdns.Msg)
		}
		out.SetRcode(req, mdns.RcodeRefused)
		return out
	}
	q := req.Question[0]
	if q.Qclass != mdns.ClassINET || !r.owns(q.Name) {
		out.SetRcode(req, mdns.RcodeRefused)
		return out
	}
	hdr := mdns.RR_Header{
		Name:   mdns.Fqdn(q.Name),
		Rrtype: q.Qtype,
		Class:  mdns.ClassINET,
		Ttl:    TTL,
	}
	var rr mdns.RR
	switch q.Qtype {
	case mdns.TypeA:
		rr = &mdns.A{Hdr: hdr, A: net.ParseIP("127.0.0.1").To4()}
	case mdns.TypeAAAA:
		rr = &mdns.AAAA{Hdr: hdr, AAAA: net.ParseIP("::1")}
	default:
		out.SetRcode(req, mdns.RcodeRefused)
		return out
	}
	out.SetReply(req)
	out.Authoritative = true
	out.Answer = []mdns.RR{rr}
	return out
}

// ServeDNS implements mdns.Handler.
func (r Resolver) ServeDNS(w mdns.ResponseWriter, req *mdns.Msg) {
	_ = w.WriteMsg(r.Answer(req))
}

func (r Resolver) owns(name string) bool {
	tld := strings.Trim(strings.ToLower(strings.TrimSpace(r.TLD)), ".")
	if tld == "" {
		return false
	}
	n := strings.ToLower(mdns.Fqdn(name))
	zone := mdns.Fqdn(tld)
	return n == zone || strings.HasSuffix(n, "."+zone)
}

// Run serves UDP and TCP on addr until ctx is cancelled. addr is host:port.
// An empty addr uses platform.DNSListenAddr.
func Run(ctx context.Context, addr, tld string) error {
	if ctx == nil {
		return errors.New("dns: context is nil")
	}
	if strings.TrimSpace(addr) == "" {
		addr = platform.DNSListenAddr()
	}
	if strings.TrimSpace(tld) == "" {
		return errors.New("dns: tld is empty")
	}
	h := Resolver{TLD: tld}
	udp := &mdns.Server{Addr: addr, Net: "udp", Handler: h}
	tcp := &mdns.Server{Addr: addr, Net: "tcp", Handler: h}
	errc := make(chan error, 2)
	go func() { errc <- udp.ListenAndServe() }()
	go func() { errc <- tcp.ListenAndServe() }()

	var err error
	select {
	case <-ctx.Done():
		err = context.Cause(ctx)
	case err = <-errc:
	}
	shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = udp.ShutdownContext(shut)
	_ = tcp.ShutdownContext(shut)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
