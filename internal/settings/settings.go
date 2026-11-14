// Package settings holds the configuration made through the app API, kept
// in a JSON file next to the hand-written rules file:
//
//	{"users": {"alice": {"allowance_seconds": 7200,
//	                     "devices": [{"type": "mac", "value": "aa:bb:cc:dd:ee:01"}]}}}
//
// A user's devices become rules ahead of the rules file, and their
// allowance overrides any allow= in it. Names are labels people give
// devices, keyed by rule ("mac aa:bb:cc:dd:ee:01", "ip 192.168.1.23/32");
// they don't affect filtering.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ascension-association/FLTR-appliance/internal/policy"
)

// Settings is the content of the settings file.
type Settings struct {
	Users map[string]User   `json:"users"`
	Names map[string]string `json:"names,omitempty"`
}

// NameKey is the key of a device's entry in Names, given a normalized
// value (see policy.Normalize).
func NameKey(typ, value string) string { return typ + " " + value }

// User is one user's settings.
type User struct {
	// AllowanceSeconds is the daily allowance; nil leaves it to the rules
	// file and -allowance, 0 means always blocked.
	AllowanceSeconds *int64   `json:"allowance_seconds"`
	Devices          []Device `json:"devices"`
}

// Device identifies one of a user's devices.
type Device struct {
	Type  string `json:"type"` // "ip" or "mac"
	Value string `json:"value"`
}

// Load reads the settings file at path. A missing file has no settings.
func Load(path string) (*Settings, error) {
	s := &Settings{Users: map[string]User{}, Names: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Users == nil {
		s.Users = map[string]User{}
	}
	if s.Names == nil {
		s.Names = map[string]string{}
	}
	return s, nil
}

// Save writes s to path atomically.
func (s *Settings) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Clone returns a deep copy of s.
func (s *Settings) Clone() *Settings {
	c := &Settings{Users: make(map[string]User, len(s.Users)), Names: maps.Clone(s.Names)}
	if c.Names == nil {
		c.Names = map[string]string{}
	}
	for name, u := range s.Users {
		if u.AllowanceSeconds != nil {
			a := *u.AllowanceSeconds
			u.AllowanceSeconds = &a
		}
		u.Devices = slices.Clone(u.Devices)
		c.Users[name] = u
	}
	return c
}

// Apply adds the settings to b: each user's devices as rules, and their
// allowance as an override.
func (s *Settings) Apply(b *policy.Builder) error {
	names := make([]string, 0, len(s.Users))
	for name := range s.Users {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		u := s.Users[name]
		if err := policy.CheckUser(name); err != nil {
			return err
		}
		for _, d := range u.Devices {
			if d.Type != "ip" && d.Type != "mac" {
				return fmt.Errorf("user %s: invalid device type %q (want ip or mac)", name, d.Type)
			}
			if err := b.Add(d.Type, d.Value, "user="+name); err != nil {
				return fmt.Errorf("user %s: %w", name, err)
			}
		}
		if u.AllowanceSeconds != nil {
			if err := b.SetAllowance(name, time.Duration(*u.AllowanceSeconds)*time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}
