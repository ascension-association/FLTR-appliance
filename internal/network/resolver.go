package network

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"strings"
	"sync"
)

// useUpstreamWhenNoNameservers makes this process's DNS lookups (blocklist
// downloads, -check) go to upstreams while resolvConf lists no nameserver.
//
// On gokrazy, /etc/resolv.conf leads to /proc/net/pnp until the DHCP client
// writes the lease's DNS servers, and a container started before then (as
// Ramekin starts its app at boot) can be left with that empty file. Go then
// falls back to a resolver on localhost, which doesn't exist, so every
// download would fail. Lookups use resolvConf's nameservers as usual once it
// has some.
func useUpstreamWhenNoNameservers(resolvConf string, upstreams []string) {
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = upstreamDialer(resolvConf, upstreams)
}

// upstreamDialer returns a net.Resolver Dial function that replaces the
// address the resolver chose with the first reachable upstream whenever
// resolvConf lists no nameserver.
func upstreamDialer(resolvConf string, upstreams []string) func(ctx context.Context, network, address string) (net.Conn, error) {
	var logOnce sync.Once
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		if hasNameserver(resolvConf) || len(upstreams) == 0 {
			return d.DialContext(ctx, network, address)
		}
		logOnce.Do(func() {
			log.Printf("dns: %s lists no nameserver; looking up names with %s", resolvConf, strings.Join(upstreams, ", "))
		})
		var errs []error
		for _, u := range upstreams {
			c, err := d.DialContext(ctx, network, u)
			if err == nil {
				return c, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
	}
}

// hasNameserver reports whether the resolv.conf file at path lists at least
// one nameserver.
func hasNameserver(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			return true
		}
	}
	return false
}
