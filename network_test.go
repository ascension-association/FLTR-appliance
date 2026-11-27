package main

import (
	"context"
	"testing"
	"time"
)

// seq returns a currentPlace stand-in that reports the given places in
// turn, then the last one forever; a zero place means "nowhere".
func seq(ps ...place) func() (place, bool) {
	i := 0
	return func() (place, bool) {
		p := ps[min(i, len(ps)-1)]
		i++
		return p, p != place{}
	}
}

func TestWaitPlaceAndMove(t *testing.T) {
	defer func(d time.Duration) { oneArmedCheck = d }(oneArmedCheck)
	oneArmedCheck = time.Millisecond
	ctx := context.Background()
	one := place{iface: "br0", steer: true}
	bridged := place{iface: "br0"}

	// Two agreeing checks in a row are needed.
	if p, ok := waitPlace(ctx, seq(place{}, one, bridged, bridged)); !ok || p != bridged {
		t.Errorf("waitPlace = %v, %v; want %v", p, ok, bridged)
	}
	// A blip to nowhere or one odd check isn't a move; two checks elsewhere are.
	if p, ok := waitMove(ctx, one, seq(one, place{}, bridged, one, bridged, bridged)); !ok || p != bridged {
		t.Errorf("waitMove = %v, %v; want %v", p, ok, bridged)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, ok := waitMove(ctx, one, seq(one, place{}, one)); ok {
		t.Error("waitMove reported a move that didn't happen")
	}
}

func TestHasFlag(t *testing.T) {
	args := []string{"-targets=192.168.1.50", "--ech", "-ipv6"}
	for name, want := range map[string]bool{"targets": true, "ech": true, "ipv6": true, "i": false, "gw": false} {
		if got := hasFlag(args, name); got != want {
			t.Errorf("hasFlag(%q) = %v, want %v", name, got, want)
		}
	}
	if !hasFlag([]string{"-i", "eth1"}, "i") || !hasFlag([]string{"-i=eth1"}, "i") {
		t.Error("hasFlag misses -i")
	}
}
