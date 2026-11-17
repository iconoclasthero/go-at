package main

import (
	"strings"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	base := buildInfo{Go: "go1.24.7", OS: "linux", Arch: "amd64"}
	rev := "8a86b72aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cases := []struct {
		mod  func(*buildInfo)
		want string
	}{
		{func(b *buildInfo) { b.Revision, b.Time, b.CGO = rev, "2026-11-16T21:03:11Z", "0" },
			"go-at 8a86b72 (2026-11-16 21:03 UTC), go1.24.7 linux/amd64, static"},
		{func(b *buildInfo) { b.Revision, b.Time, b.CGO, b.Modified = rev, "2026-11-16T21:03:11Z", "1", true },
			"go-at 8a86b72+modified (2026-11-16 21:03 UTC), go1.24.7 linux/amd64, dynamic (cgo)"},
		{func(b *buildInfo) { b.Version, b.Revision = "v1.2.3", rev },
			"go-at v1.2.3 8a86b72, go1.24.7 linux/amd64"},
		{func(b *buildInfo) { b.Version = "v1.2.3" },
			"go-at v1.2.3, go1.24.7 linux/amd64"},
		{func(b *buildInfo) { b.CGO = "0" },
			"go-at (unknown commit: not built from a git checkout), go1.24.7 linux/amd64, static"},
	}
	for _, c := range cases {
		b := base
		c.mod(&b)
		if got := formatVersion(b); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	e := newEnv(t)
	for _, c := range [][]string{{"at", "--version"}, {"at", "-V"}, {"atq", "--version"}, {"atrm", "--version"}, {"batch", "-V"}, {"atd", "--version"}} {
		out, se, code := e.run("", c[0], c[1:]...)
		if code != 0 || !strings.HasPrefix(out, "go-at ") || strings.Count(out, "\n") != 1 || se != "" {
			t.Errorf("%v: code %d stdout %q stderr %q", c, code, out, se)
		}
	}
	// it needs no spool, no config and no daemon, and does not create anything
	if len(e.jobs()) != 0 {
		t.Errorf("--version must not touch the spool")
	}
}
