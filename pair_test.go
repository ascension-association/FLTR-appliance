package main

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/ascension-association/FLTR-appliance/internal/api"
)

func TestAPIAddresses(t *testing.T) {
	for _, tc := range []struct {
		flag []string
		want []string
		err  bool
	}{
		{nil, []string{":8443"}, false},
		{[]string{"off"}, nil, false},
		{[]string{"192.168.1.1:8443", "[fd00::1]:8443"}, []string{"192.168.1.1:8443", "[fd00::1]:8443"}, false},
		{[]string{"off", ":8443"}, nil, true},
		{[]string{"203.0.113.1:8443"}, nil, true},
		{[]string{"192.168.1.1"}, nil, true}, // no port
	} {
		got, err := apiAddresses(tc.flag)
		if (err != nil) != tc.err || !slices.Equal(got, tc.want) {
			t.Errorf("apiAddresses(%q) = %q, %v", tc.flag, got, err)
		}
	}
}

func TestPairingURL(t *testing.T) {
	home := &api.HomeNet{
		Interfaces: func() ([]api.Interface, error) {
			return []api.Interface{
				{Name: "uplink0", Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.0.5/24")}},
				{Name: "lan0", Addrs: []netip.Prefix{netip.MustParsePrefix("fd00::1/64"), netip.MustParsePrefix("192.168.1.1/24")}},
			}, nil
		},
		DefaultRoutes: func() map[string]bool { return map[string]bool{"uplink0": true} },
	}
	for _, tc := range []struct {
		apiURL string
		addrs  []string
		want   string
	}{
		{"", []string{":8443"}, "https://192.168.1.1:8443"}, // the LAN, not the private uplink
		{"", []string{"[::]:9000"}, "https://192.168.1.1:9000"},
		{"", []string{"[fd00::1]:8443"}, "https://[fd00::1]:8443"},
		{"https://router.lan:8443", []string{":8443"}, "https://router.lan:8443"},
	} {
		if got, ok := pairingURL(tc.apiURL, tc.addrs, home); !ok || got != tc.want {
			t.Errorf("pairingURL(%q, %q) = %q, %v; want %q", tc.apiURL, tc.addrs, got, ok, tc.want)
		}
	}
	empty := &api.HomeNet{
		Interfaces:    func() ([]api.Interface, error) { return nil, nil },
		DefaultRoutes: func() map[string]bool { return nil },
	}
	if _, ok := pairingURL("", []string{":8443"}, empty); ok {
		t.Error("pairingURL without a LAN address: ok")
	}
}

func TestUnpairOnce(t *testing.T) {
	dir := t.TempDir()
	c1, _, err := api.LoadCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := unpairOnce(dir, "2026-11-12")
	if err != nil || !rotated {
		t.Fatalf("first unpair: rotated=%v err=%v; want true, nil", rotated, err)
	}
	c2, created, err := api.LoadCredentials(dir)
	if err != nil || !created || c2.Token == c1.Token {
		t.Fatalf("after unpair: created=%v err=%v, token changed=%v; want a new token", created, err, c2.Token != c1.Token)
	}
	if c2.Pin != c1.Pin {
		t.Errorf("unpair changed the certificate pin")
	}
	// The same id again, as after a restart with the flag still set: no-op.
	if rotated, err := unpairOnce(dir, "2026-11-12"); err != nil || rotated {
		t.Fatalf("repeated unpair: rotated=%v err=%v; want false, nil", rotated, err)
	}
	c3, _, _ := api.LoadCredentials(dir)
	if c3.Token != c2.Token {
		t.Errorf("repeated unpair replaced the token")
	}
	// A new id unpairs again.
	if rotated, err := unpairOnce(dir, "2026-12-01"); err != nil || !rotated {
		t.Fatalf("new id: rotated=%v err=%v; want true, nil", rotated, err)
	}
}
