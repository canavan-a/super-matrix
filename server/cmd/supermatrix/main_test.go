package main

import (
	"strings"
	"testing"
)

func TestTargets(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string // base URL, or the start of the error
	}{
		{[]string{"ssh-box.example.com"}, "https://ssh-box.example.com"},
		{[]string{"--url", "http://box.localhost:8080", "box.localhost"}, "http://box.localhost:8080"},
		{[]string{"--url", "http://127.0.0.1:8080", "x"}, "http://127.0.0.1:8080"},
		{[]string{"--url", "http://ssh-box.example.com", "x"}, "refusing plain http"},
		{[]string{"--url", "ftp://x", "x"}, "bad URL"},
	} {
		got, err := parseTarget("access", c.args, nil)
		if err != nil {
			if !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("%v: %v", c.args, err)
			}
			continue
		}
		if got.base.String() != c.want {
			t.Errorf("%v: %s", c.args, got.base)
		}
	}
}

func TestSessionShape(t *testing.T) {
	for v, ok := range map[string]bool{
		"YWJjfDE3MDAwMDAwMDB8ZA.mac_-123": true,
		"abc":                             false,
		"a.b\r\nX-Evil: 1":                false,
		"a.b; other=1":                    false,
	} {
		if sessionRe.MatchString(v) != ok {
			t.Errorf("%q: want %v", v, ok)
		}
	}
}
