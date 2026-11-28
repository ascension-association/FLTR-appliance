package filter

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

// Start must return only once both listeners answer, since network
// protection redirects the LAN's DNS here as soon as it returns.
func TestStartAnswersOnReturn(t *testing.T) {
	// Find a port free for both UDP and TCP.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	ss := NewSafeSearch("127.0.0.1:1", "127.0.0.1:1") // no upstream needed
	bl := NewBlocklist()
	bl.Add("blocked.test")
	ss.SetBlocklist(bl)
	errc, err := ss.Start(addr)
	if err != nil {
		t.Fatal(err)
	}

	for _, network := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: network}
		m := new(dns.Msg)
		m.SetQuestion("www.blocked.test.", dns.TypeA)
		r, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("%s: query right after Start: %v", network, err)
		}
		if r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError {
			t.Errorf("%s: rcode %s, want a sinkhole answer", network, dns.RcodeToString[r.Rcode])
		}
	}

	if _, err := ss.Start(addr); err == nil {
		t.Error("second Start on the same address succeeded, want an error")
	}
	select {
	case err := <-errc:
		t.Errorf("first resolver stopped: %v", err)
	default:
	}
}
