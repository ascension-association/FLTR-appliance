package netinfo

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBridgeOf(t *testing.T) {
	sysfs = t.TempDir()
	t.Cleanup(func() { sysfs = "/sys" })
	mk := func(p, content string) {
		p = filepath.Join(sysfs, "class", "net", p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if content == "" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("br0/bridge", "")
	mk("br0/brif/eth1", "")
	mk("br0/brif/eth0", "")
	mk("eth0/carrier", "1\n")
	mk("eth1/carrier", "0\n")
	mk("eth2/carrier", "1\n")

	if _, ok := bridgeOf("eth2"); ok {
		t.Error("eth2 is not a bridge")
	}
	b, ok := bridgeOf("br0")
	if !ok || b.Name != "br0" || !slices.Equal(b.Ports, []string{"eth0", "eth1"}) || !slices.Equal(b.Linked, []string{"eth0"}) {
		t.Errorf("bridgeOf(br0) = %+v, %v; want ports eth0,eth1 with eth0 linked", b, ok)
	}
	mk("eth1/carrier", "1\n")
	if b, _ := bridgeOf("br0"); !slices.Equal(b.Linked, []string{"eth0", "eth1"}) {
		t.Errorf("linked = %v; want eth0,eth1", b.Linked)
	}
}
