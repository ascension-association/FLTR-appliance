package netinfo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sysfs is where interface state is read from; tests point it elsewhere.
var sysfs = "/sys"

// Bridge describes this host's uplink when it is a Linux bridge.
type Bridge struct {
	Name   string   // the bridge interface, e.g. br0
	Ports  []string // its member interfaces, sorted
	Linked []string // the ports that have a carrier (a cable plugged in)
}

// Bridging reports whether the box sits in the wire as a transparent bridge:
// its IPv4 default route is on a bridge with at least two linked ports, so
// the LAN's traffic crosses the box on its own and needs no ARP steering. The
// bridge is returned even when it has fewer linked ports, so callers can tell
// "a bridge with one cable" (which behaves like a one-interface box) apart.
func Bridging() (Bridge, bool) {
	for _, r := range DefaultRoutes() {
		if !r.Gateway.Is4() {
			continue
		}
		if b, ok := bridgeOf(r.Iface); ok {
			return b, len(b.Linked) >= 2
		}
	}
	return Bridge{}, false
}

// bridgeOf returns iface's ports if it is a bridge.
func bridgeOf(iface string) (Bridge, bool) {
	dir := filepath.Join(sysfs, "class", "net", iface)
	if _, err := os.Stat(filepath.Join(dir, "bridge")); err != nil {
		return Bridge{}, false
	}
	b := Bridge{Name: iface}
	ents, _ := os.ReadDir(filepath.Join(dir, "brif"))
	for _, e := range ents {
		b.Ports = append(b.Ports, e.Name())
	}
	slices.Sort(b.Ports)
	for _, p := range b.Ports {
		c, err := os.ReadFile(filepath.Join(sysfs, "class", "net", p, "carrier"))
		if err == nil && strings.TrimSpace(string(c)) == "1" {
			b.Linked = append(b.Linked, p)
		}
	}
	return b, true
}
