package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ascension-association/FLTR-appliance/internal/netinfo"
)

// Bridge mode: the box sits in the wire between the router and the LAN, with
// both ports in a Linux bridge (br0) that carries its own address. Bridged
// frames skip the IP stack, so fltr's forward chain sees them only through
// br_netfilter (CONFIG_BRIDGE_NETFILTER), with these sysctls on.

const bridgeSysctlDir = "/proc/sys/net/bridge"

var bridgeSysctls = []string{"bridge-nf-call-iptables", "bridge-nf-call-ip6tables"}

// setBridgeFilter turns on (or off) passing bridged IPv4 and IPv6 traffic
// through the inet nftables hooks, and checks that it took effect.
//
// It's on only while two ports are bridged. With one, nothing is bridged and
// network protection routes steered traffic in and out of the bridge, and
// br_netfilter can then lose most of those routed packets on the way out
// (seen on the R76S's 7.3.1 kernel), so it's turned off.
func setBridgeFilter(dir string, on bool) error {
	if _, err := os.Stat(dir); err != nil {
		if !on {
			return nil // no br_netfilter: nothing to turn off
		}
		return fmt.Errorf("%s is missing: the kernel needs CONFIG_BRIDGE_NETFILTER=y", dir)
	}
	v := "0"
	if on {
		v = "1"
	}
	for _, name := range bridgeSysctls {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			return fmt.Errorf("setting %s to %s: %w", name, v, err)
		}
		if b, err := os.ReadFile(p); err != nil || strings.TrimSpace(string(b)) != v {
			return fmt.Errorf("%s=%s did not take effect", name, v)
		}
	}
	return nil
}

// bridgeWatch keeps bridged traffic visible to fltr and logs when the box
// moves in or out of bridge mode (a second cable plugged in or pulled).
type bridgeWatch struct {
	dir   string                        // bridge sysctls; bridgeSysctlDir
	state func() (netinfo.Bridge, bool) // netinfo.Bridging
	logf  func(format string, args ...any)
	last  string // last state logged
}

func newBridgeWatch() *bridgeWatch {
	return &bridgeWatch{dir: bridgeSysctlDir, state: netinfo.Bridging, logf: log.Printf}
}

// check turns bridge filtering on while the box is bridging and off while its
// bridge has only one linked port. It fails only while the box is bridging
// and filtering can't be turned on: bridged traffic would then pass
// unfiltered, so fltr refuses to run (and its health check fails) rather
// than look healthy.
func (w *bridgeWatch) check() error {
	b, bridging := w.state()
	var msg string
	var err error
	if b.Name != "" {
		err = setBridgeFilter(w.dir, bridging)
	}
	switch {
	case b.Name == "":
		msg = ""
	case bridging && err != nil:
		return fmt.Errorf("bridge mode: %s bridges %s, but bridged traffic can't be filtered: %v", b.Name, strings.Join(b.Linked, " and "), err)
	case bridging:
		msg = fmt.Sprintf("bridge mode: %s bridges %s; filtering bridged traffic", b.Name, strings.Join(b.Linked, " and "))
	case err != nil:
		msg = fmt.Sprintf("%s has %d of its ports linked, so it works like a one-interface box, but turning off bridge filtering failed, so steered traffic may be lost: %v", b.Name, len(b.Linked), err)
	default:
		msg = fmt.Sprintf("%s has %d of its ports linked, so it works like a one-interface box; plug in a second cable for bridge mode", b.Name, len(b.Linked))
	}
	if msg != w.last {
		if msg == "" {
			w.logf("bridge mode off: the uplink is no longer a bridge")
		} else {
			w.logf("%s", msg)
		}
		w.last = msg
	}
	return nil
}
