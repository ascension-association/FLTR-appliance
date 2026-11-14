package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ascension-association/FLTR-appliance/internal/api"
)

// defaultAPIDir is under /data, the directory Ramekin keeps across updates
// and rollbacks.
const defaultAPIDir = "/data/api"

// pair shows the QR code that pairs the app with fltr's API.
func pair(args []string) error {
	fs := flag.NewFlagSet("fltr pair", flag.ExitOnError)
	var addrs listFlag
	fs.Var(&addrs, "api", "fltr's -api setting (default :8443)")
	dir := fs.String("api-dir", defaultAPIDir, "the API's directory, as given to fltr")
	apiURL := fs.String("api-url", "", "URL the app uses to reach the API (default: this host's LAN address)")
	png := fs.String("png", "", "also write the QR code to this PNG file")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: fltr pair [flags]\n\nShows the QR code the app scans to pair with fltr.\n\nflags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	addrs, err := apiAddresses(addrs)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return errors.New("pair: the app API is off")
	}
	u, ok := pairingURL(*apiURL, addrs, &api.HomeNet{})
	if !ok {
		return errors.New("pair: no LAN address found; give -api-url, e.g. -api-url=https://192.168.1.1:8443")
	}
	creds, _, err := api.LoadCredentials(*dir)
	if err != nil {
		return err
	}
	return printPairing(os.Stdout, creds, u, *png)
}

// apiAddresses returns the addresses the API listens on given the -api
// flag: :8443 by default, none for "off". Public addresses are refused.
func apiAddresses(flag []string) ([]string, error) {
	switch {
	case len(flag) == 0:
		return []string{":8443"}, nil
	case slices.Contains(flag, "off"):
		if len(flag) > 1 {
			return nil, errors.New("-api=off cannot be combined with addresses")
		}
		return nil, nil
	}
	for _, a := range flag {
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			return nil, fmt.Errorf("invalid -api %q: %w", a, err)
		}
		if ip, err := netip.ParseAddr(host); err == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return nil, fmt.Errorf("invalid -api %q: a public address; the app API is for the home network only", a)
		}
	}
	return flag, nil
}

// printPairing writes the pairing QR code and URI to w, and the QR code to
// the PNG file png unless it is empty.
func printPairing(w io.Writer, creds *api.Credentials, baseURL, png string) error {
	uri := creds.PairingURI(baseURL)
	text, err := api.QRText(uri)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Scan with the gk-fltr app to pair it with %s:\n%s\n%s\n"+
		"Keep this secret: it grants control of fltr. To unpair all apps, start fltr with -unpair=<any new value>, e.g. today's date.\n",
		baseURL, text, uri)
	if png != "" {
		b, err := api.QRPNG(uri)
		if err != nil {
			return err
		}
		if err := os.WriteFile(png, b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pairingURL returns the URL the app should use: apiURL if set, else one
// built from the first API address, using this host's first home network
// address if it names none. ok is false if there is no such address yet.
func pairingURL(apiURL string, addrs []string, home *api.HomeNet) (u string, ok bool) {
	if apiURL != "" {
		return apiURL, true
	}
	host, port, err := net.SplitHostPort(addrs[0])
	if err != nil {
		return "", false
	}
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		as := home.Addrs()
		if len(as) == 0 {
			return "", false
		}
		host = as[0].String()
	}
	return "https://" + net.JoinHostPort(host, port), true
}

// logPairingWhenReady logs the pairing QR code now if the API's URL is
// known, or else once the device has a home network address. Ramekin has no
// shell for running "fltr pair" on the device, so the device's log
// (`ramekin logs`) is where the QR code is read.
func logPairingWhenReady(creds *api.Credentials, apiURL string, addrs []string, home *api.HomeNet) error {
	if u, ok := pairingURL(apiURL, addrs, home); ok {
		return printPairing(log.Writer(), creds, u, "")
	}
	log.Printf("app API: no home network address yet; the pairing QR code is logged once there is one")
	go func() {
		for {
			time.Sleep(5 * time.Second)
			if u, ok := pairingURL(apiURL, addrs, &api.HomeNet{}); ok {
				if err := printPairing(log.Writer(), creds, u, ""); err != nil {
					log.Printf("app API: %v", err)
				}
				return
			}
		}
	}()
	return nil
}

// unpairOnce deletes the API token in dir, so a new one is made and every
// paired app loses access, unless it already did so for id. Ramekin devices
// have no shell for deleting the token, and flags stay set across restarts,
// so the id makes -unpair act once rather than at every start.
func unpairOnce(dir, id string) (bool, error) {
	marker := filepath.Join(dir, "unpaired")
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == id {
		return false, nil
	}
	if err := os.Remove(filepath.Join(dir, "token")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	return true, os.WriteFile(marker, []byte(id+"\n"), 0o600)
}
