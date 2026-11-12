package main

import (
	"errors"
	"strings"
)

// Ramekin configures a device's container through environment variables
// (`ramekin device set <device> --env KEY=VALUE`), so run also reads these.
const (
	// argsEnv holds extra flags, parsed after the command line so they
	// override the image's runscript, e.g.
	// FLTR_ARGS=-fail-open "-network-args=-targets=192.168.1.50 -ech".
	argsEnv = "FLTR_ARGS"
	// rulesEnv holds rules in the rules-file syntax, separated by newlines
	// or semicolons, e.g. FLTR_RULES=mac aa:bb:cc:dd:ee:01 user=alice allow=2h; ip 192.168.1.0/28
	rulesEnv = "FLTR_RULES"
)

// splitArgs splits s into words like a shell would for simple cases:
// whitespace separates words, and single or double quotes group them.
// Backslash escapes the next character outside single quotes.
func splitArgs(s string) ([]string, error) {
	var (
		args  []string
		cur   strings.Builder
		inArg bool
		quote rune
	)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\' && quote != '\'':
			if i+1 == len(rs) {
				return nil, errors.New("trailing backslash")
			}
			i++
			cur.WriteRune(rs[i])
			inArg = true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

// inlineRules turns FLTR_RULES into rules-file text: semicolons separate
// rules like newlines do.
func inlineRules(s string) string {
	return strings.ReplaceAll(s, ";", "\n")
}
