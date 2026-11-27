package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ascension-association/FLTR-appliance/internal/netinfo"
	"github.com/ascension-association/FLTR-appliance/internal/network"
)

// oneArmedCheck is how long the box must keep looking like it sits next
// to the router (or in the wire, bridging) before -network=auto starts
// network protection there. A router can look one-armed briefly at boot,
// before its LAN interface has an address, and network protection there
// would steer the uplink's network.
var oneArmedCheck = 15 * time.Second

// place is where network protection runs: on which interface, and whether
// it steers the LAN's traffic there with ARP (a one-interface box) or finds
// it already crossing the box (bridge mode).
type place struct {
	iface string
	steer bool
}

func (p place) String() string {
	if p.steer {
		return "one-interface box on " + p.iface
	}
	return "bridge mode on " + p.iface
}

// currentPlace reports where network protection belongs right now.
func currentPlace() (place, bool) {
	if b, ok := netinfo.Bridging(); ok {
		return place{iface: b.Name}, true
	}
	if iface, ok := netinfo.OneArmed(); ok {
		return place{iface: iface, steer: true}, true
	}
	return place{}, false
}

// superviseNetwork runs network protection until ctx ends. With auto, it
// first waits until this box is a one-interface box next to the router or a
// bridge in the wire, runs it there (on that interface unless args name one,
// without ARP steering when bridging), and restarts it when the box moves
// from one to the other: a second cable plugged in, or one pulled.
func superviseNetwork(ctx context.Context, auto bool, args []string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("network: %v", err)
		return
	}
	if !auto {
		runNetwork(ctx, exe, args)
		return
	}
	for {
		p, ok := waitPlace(ctx, currentPlace)
		if !ok {
			return
		}
		a := args
		if !p.steer && !hasFlag(a, "steer") {
			a = append([]string{"-steer=false"}, a...)
		}
		if !hasFlag(a, "i") {
			a = append([]string{"-i", p.iface}, a...)
		}
		runCtx, stop := context.WithCancel(ctx)
		moved := make(chan place, 1)
		go func() {
			if np, ok := waitMove(runCtx, p, currentPlace); ok {
				moved <- np
				stop()
			}
		}()
		runNetwork(runCtx, exe, a)
		stop()
		if ctx.Err() != nil {
			return
		}
		select {
		case np := <-moved:
			log.Printf("network protection: the box changed from %s to %s; restarting it", p, np)
		default:
		}
	}
}

func runNetwork(ctx context.Context, exe string, args []string) {
	log.Printf("starting network protection %s", strings.Join(args, " "))
	s := network.Supervisor{
		Socket: network.SocketName,
		Command: func() *exec.Cmd {
			return exec.Command(exe, append([]string{"network", "-supervised"}, args...)...)
		},
		Logf:    log.Printf,
		Backoff: 10 * time.Second,
	}
	s.Run(ctx)
}

// waitPlace returns where network protection belongs once two checks
// oneArmedCheck apart agree, or false if ctx ends first.
func waitPlace(ctx context.Context, cur func() (place, bool)) (place, bool) {
	logged := false
	var prev place
	for {
		p, ok := cur()
		switch {
		case ok && p == prev:
			return p, true
		case ok:
			prev = p
		default:
			prev = place{}
			if !logged {
				log.Printf("network protection not started: this box doesn't look like a one-interface box next to the router, or a bridge between the router and the LAN (-network=on starts it anyway)")
				logged = true
			}
		}
		if !sleepCtx(ctx, oneArmedCheck) {
			return place{}, false
		}
	}
}

// waitMove returns once the box has been somewhere other than p (and
// somewhere network protection belongs) on two checks in a row. Losing its
// place entirely, for example while the default route is briefly gone, isn't
// a move: network protection keeps running.
func waitMove(ctx context.Context, p place, cur func() (place, bool)) (place, bool) {
	var prev place
	for sleepCtx(ctx, oneArmedCheck) {
		np, ok := cur()
		switch {
		case !ok || np == p:
			prev = place{}
		case np == prev:
			return np, true
		default:
			prev = np
		}
	}
	return place{}, false
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// hasFlag reports whether args set the flag name.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		a = strings.TrimLeft(a, "-")
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}
