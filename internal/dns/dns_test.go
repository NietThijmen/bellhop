package dns

import (
	"context"
	"net"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

func TestAnswer(t *testing.T) {
	r := Resolver{TLD: "test"}
	a := ask(t, r, "app.test.", mdns.TypeA)
	if a.Rcode != mdns.RcodeSuccess || !a.Authoritative || len(a.Answer) != 1 {
		t.Fatalf("app.test A = %+v", a)
	}
	rec, ok := a.Answer[0].(*mdns.A)
	if !ok || !rec.A.Equal(net.ParseIP("127.0.0.1")) || rec.Hdr.Ttl != TTL || rec.Hdr.Name != "app.test." {
		t.Fatalf("A rr = %+v", a.Answer[0])
	}

	apex := ask(t, r, "test.", mdns.TypeAAAA)
	aaaa, ok := apex.Answer[0].(*mdns.AAAA)
	if apex.Rcode != mdns.RcodeSuccess || !ok || !aaaa.AAAA.Equal(net.ParseIP("::1")) || aaaa.Hdr.Ttl != 60 {
		t.Fatalf("apex AAAA = %+v", apex)
	}

	deep := ask(t, r, "A.B.Test.", mdns.TypeA)
	if deep.Rcode != mdns.RcodeSuccess || deep.Answer[0].Header().Name != "A.B.Test." {
		t.Fatalf("deep name = %+v", deep)
	}

	for _, name := range []string{"example.com.", "com.", "nottest.", "test.com.", "latest."} {
		got := ask(t, r, name, mdns.TypeA)
		if got.Rcode != mdns.RcodeRefused || len(got.Answer) != 0 {
			t.Errorf("%s rcode = %d answers = %d", name, got.Rcode, len(got.Answer))
		}
	}
	mx := ask(t, r, "app.test.", mdns.TypeMX)
	if mx.Rcode != mdns.RcodeRefused || len(mx.Answer) != 0 {
		t.Fatalf("MX = %+v", mx)
	}
	if got := (Resolver{}).Answer(askMsg("app.test.", mdns.TypeA)); got.Rcode != mdns.RcodeRefused {
		t.Fatalf("empty tld rcode = %d", got.Rcode)
	}
	if got := r.Answer(new(mdns.Msg)); got.Rcode != mdns.RcodeRefused {
		t.Fatalf("no question rcode = %d", got.Rcode)
	}
}

func TestListenLoopback(t *testing.T) {
	addr, cancel, errc := runOnFreePort(t)
	defer cancel()

	tcp := &mdns.Client{Net: "tcp", Timeout: 200 * time.Millisecond}
	got, _, err := tcp.Exchange(askMsg("other.example.", mdns.TypeA), addr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rcode != mdns.RcodeRefused {
		t.Fatalf("tcp rcode = %d", got.Rcode)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resolver did not stop")
	}
}

// freeDNSAddr returns 127.0.0.1:<port> where the port was free for both
// UDP and TCP a moment ago.
func freeDNSAddr(t *testing.T) string {
	t.Helper()
	for range 20 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		pc, err := net.ListenPacket("udp", addr)
		_ = ln.Close()
		if err != nil {
			continue
		}
		_ = pc.Close()
		return addr
	}
	t.Fatal("no port free for both UDP and TCP")
	return ""
}

// runOnFreePort starts the resolver on a free port and waits until it
// answers over UDP. If Run fails (another process won the bind race), it
// retries on a new port; any other failure is reported with Run's error.
func runOnFreePort(t *testing.T) (addr string, cancel context.CancelFunc, errc <-chan error) {
	t.Helper()
	udp := &mdns.Client{Net: "udp", Timeout: 200 * time.Millisecond}
	var last error
	for range 5 {
		addr = freeDNSAddr(t)
		ctx, stop := context.WithCancel(context.Background())
		ch := make(chan error, 1)
		go func() { ch <- Run(ctx, addr, "test") }()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-ch:
				last = err
				deadline = time.Time{} // Run exited: retry on a new port
				continue
			default:
			}
			got, _, err := udp.Exchange(askMsg("site.test.", mdns.TypeA), addr)
			if err == nil && got != nil && got.Rcode == mdns.RcodeSuccess {
				return addr, stop, ch
			}
			last = err
			time.Sleep(15 * time.Millisecond)
		}
		stop()
	}
	t.Fatalf("resolver never answered: %v", last)
	return "", nil, nil
}

func ask(t *testing.T, r Resolver, name string, qtype uint16) *mdns.Msg {
	t.Helper()
	return r.Answer(askMsg(name, qtype))
}

func askMsg(name string, qtype uint16) *mdns.Msg {
	m := new(mdns.Msg)
	m.SetQuestion(mdns.Fqdn(name), qtype)
	return m
}
