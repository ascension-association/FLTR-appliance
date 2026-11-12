package filter

import (
	"os"
	"testing"

	"github.com/google/nftables"
)

// TestNewIPBlockerRemovesLegacyTable needs CAP_NET_ADMIN in a throwaway
// network namespace, e.g.
//
//	FLTR_NFT_TEST=1 unshare -Urn go test -run Legacy ./internal/filter
func TestNewIPBlockerRemovesLegacyTable(t *testing.T) {
	if os.Getenv("FLTR_NFT_TEST") == "" {
		t.Skip("set FLTR_NFT_TEST=1 and run in a network namespace with CAP_NET_ADMIN")
	}
	c, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: legacyTableName})
	if err := c.Flush(); err != nil {
		t.Fatalf("creating the legacy table: %v", err)
	}
	if _, err := NewIPBlocker(""); err != nil {
		t.Fatal(err)
	}
	tables, err := c.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range tables {
		names = append(names, tb.Name)
	}
	if len(names) != 1 || names[0] != "fltr_network" {
		t.Errorf("tables = %q, want only fltr_network", names)
	}
}
