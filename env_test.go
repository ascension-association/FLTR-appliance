package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ascension-association/FLTR-appliance/internal/settings"
)

func TestSplitArgs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  -fail-open  ", []string{"-fail-open"}},
		{"-fail-open -timezone=America/Denver", []string{"-fail-open", "-timezone=America/Denver"}},
		{`"-network-args=-targets=192.168.1.50 -ech" -v`, []string{"-network-args=-targets=192.168.1.50 -ech", "-v"}},
		{`-network-args='-i eth0' -x`, []string{"-network-args=-i eth0", "-x"}},
		{`-network-args="-i eth0"`, []string{"-network-args=-i eth0"}},
		{`a\ b ''`, []string{"a b", ""}},
		{`'it''s'`, []string{"its"}},
	} {
		got, err := splitArgs(tc.in)
		if err != nil {
			t.Errorf("splitArgs(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{`"open`, `'open`, `trailing\`} {
		if _, err := splitArgs(bad); err == nil {
			t.Errorf("splitArgs(%q) succeeded, want an error", bad)
		}
	}
}

func TestInlineRules(t *testing.T) {
	c := common{inlineRules: inlineRules(
		"mac aa:bb:cc:dd:ee:01 user=alice allow=2h; ip 192.168.1.23 user=alice;ip 192.168.1.0/28")}
	p, err := c.policyWith(&settings.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.String(), "2 ip, 1 mac, 0 ja4 rules, 1 users"; got != want {
		t.Errorf("policy = %q, want %q", got, want)
	}
	if a, ok := p.Allowance("user:alice"); !ok || a != 2*time.Hour {
		t.Errorf("alice's allowance = %v, %v; want 2h, true", a, ok)
	}

	c.inlineRules = inlineRules("bogus 1.2.3.4")
	if _, err := c.policyWith(&settings.Settings{}); err == nil || !strings.Contains(err.Error(), rulesEnv) {
		t.Errorf("bad rule: err = %v, want one naming %s", err, rulesEnv)
	}
}
