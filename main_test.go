package main

import (
	"strings"
	"testing"

	"playah/internal/radio"
)

func TestStartURL(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		cfg  radio.Config
		want string
	}{
		{"argument with auto play", "http://a", radio.Config{AutoPlay: true, LastURL: "http://b"}, "http://a"},
		{"argument without auto play", " http://a ", radio.Config{LastURL: "http://b"}, "http://a"},
		{"last stream with auto play", "", radio.Config{AutoPlay: true, LastURL: "http://b"}, "http://b"},
		{"last stream without auto play", "  ", radio.Config{LastURL: "http://b"}, ""},
		{"nothing", "", radio.Config{AutoPlay: true}, ""},
	}
	for _, c := range cases {
		if got := startURL(c.arg, c.cfg); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestUserAgentCarriesTheVersion(t *testing.T) {
	if got := version(); got == "" || got == "(devel)" {
		t.Errorf("version = %q", got)
	}
	if got := userAgent(); !strings.HasPrefix(got, "playah/") || strings.HasSuffix(got, "/") {
		t.Errorf("user agent = %q", got)
	}
}
