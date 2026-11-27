package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ascension-association/FLTR-appliance/internal/netinfo"
)

func TestBridgeWatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bridge")
	var logged []string
	var b netinfo.Bridge
	var bridging bool
	w := &bridgeWatch{
		dir:   dir,
		state: func() (netinfo.Bridge, bool) { return b, bridging },
		logf:  func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) },
	}
	step := func(name string, wantErr bool, wantLog string) {
		t.Helper()
		logged = nil
		err := w.check()
		if (err != nil) != wantErr {
			t.Errorf("%s: err = %v, want error %v", name, err, wantErr)
		}
		got := strings.Join(logged, "\n")
		if (wantLog == "") != (got == "") || !strings.Contains(got, wantLog) {
			t.Errorf("%s: logged %q, want %q", name, got, wantLog)
		}
	}

	step("no bridge", false, "")
	b = netinfo.Bridge{Name: "br0", Ports: []string{"eth0", "eth1"}, Linked: []string{"eth0"}}
	step("one port, no br_netfilter", false, "plug in a second cable")
	bridging, b.Linked = true, []string{"eth0", "eth1"}
	step("bridging, no br_netfilter", true, "")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range bridgeSysctls {
		os.WriteFile(filepath.Join(dir, n), []byte("0\n"), 0o644)
	}
	step("bridging", false, "br0 bridges eth0 and eth1")
	for _, n := range bridgeSysctls {
		if v, _ := os.ReadFile(filepath.Join(dir, n)); strings.TrimSpace(string(v)) != "1" {
			t.Errorf("%s = %q, want 1", n, v)
		}
	}
	step("still bridging", false, "")
	bridging, b.Linked = false, []string{"eth1"}
	step("cable pulled", false, "plug in a second cable")
	for _, n := range bridgeSysctls {
		if v, _ := os.ReadFile(filepath.Join(dir, n)); strings.TrimSpace(string(v)) != "0" {
			t.Errorf("with one port, %s = %q, want 0", n, v)
		}
	}
	b = netinfo.Bridge{}
	step("bridge gone", false, "bridge mode off")
}
