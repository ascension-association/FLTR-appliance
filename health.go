package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ascension-association/FLTR-appliance/internal/firewall"
)

// defaultReadyFile is written once filtering has started. It lives on /run,
// which Ramekin mounts as a fresh tmpfs for every container start, so it
// never survives from an earlier run the way the fail-closed nftables table
// does.
const defaultReadyFile = "/run/fltr/ready"

// markReady records in path that fltr is filtering, in mode, with the
// nftables table it installed.
func markReady(path, table, mode string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(table+" "+mode+"\n"), 0o644)
}

// health exits 0 when this container's fltr has started filtering and its
// nftables table is installed. Ramekin runs it inside the container as the
// release health check (`--health-exec "fltr health"`), so a release that
// can't filter (for example, queue mode missing from the kernel) is rolled
// back instead of committed.
func health(args []string) error {
	fs := flag.NewFlagSet("fltr health", flag.ExitOnError)
	readyFile := fs.String("ready-file", defaultReadyFile, "fltr's -ready-file setting")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: fltr health [flags]\n\nExits 0 once fltr is filtering and its nftables table is installed.\n\nflags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	b, err := os.ReadFile(*readyFile)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("fltr has not started filtering yet")
	}
	if err != nil {
		return err
	}
	table, mode, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	ok, err := firewall.TableExists(table)
	if err != nil {
		return fmt.Errorf("checking nftables table %q: %w", table, err)
	}
	if !ok {
		return fmt.Errorf("nftables table %q is missing", table)
	}
	fmt.Printf("ok: filtering in %s mode (table %s)\n", mode, table)
	return nil
}
