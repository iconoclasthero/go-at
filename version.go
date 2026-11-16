package main

// go-at --version (also -V, and on any of the go-* names).
//
// Nothing needs to be set at build time: since Go 1.18 "go build" inside a git
// checkout records the commit, its time and whether the tree had uncommitted
// changes, and this reads it back. A release build can still name itself:
//
//	go build -ldflags "-X main.version=v1.2.3" .
//
// Built from a tarball or with -buildvcs=false there is no commit to report,
// and the output says so rather than guessing.

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version may be set with -ldflags "-X main.version=...".
var version string

type buildInfo struct {
	Version  string // from -ldflags, may be empty
	Revision string // full commit hash, empty if unknown
	Time     string // commit time, RFC 3339
	Modified bool   // the tree had uncommitted changes
	CGO      string // "1" or "0", empty if unknown
	Go       string
	OS, Arch string
}

// formatVersion renders one line, e.g.
//
//	go-at 8a86b72 (2026-10-25 14:03 UTC), go1.24.7 linux/amd64, static
//	go-at v1.2.3 8a86b72+modified (2026-10-25 14:03 UTC), go1.24.7 linux/amd64, dynamic (cgo)
//	go-at (unknown commit: not built from a git checkout), go1.24.7 linux/amd64, static
//
// "+modified" means the binary was built from a tree with uncommitted
// changes, so the commit alone does not say what is in it.
func formatVersion(b buildInfo) string {
	var parts []string
	if b.Version != "" {
		parts = append(parts, b.Version)
	}
	switch {
	case b.Revision != "":
		rev := b.Revision
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if b.Modified {
			rev += "+modified"
		}
		if t := shortTime(b.Time); t != "" {
			rev += " (" + t + ")"
		}
		parts = append(parts, rev)
	case b.Version == "":
		parts = append(parts, "(unknown commit: not built from a git checkout)")
	}
	s := "go-at " + strings.Join(parts, " ")
	s += ", " + b.Go + " " + b.OS + "/" + b.Arch
	switch b.CGO {
	case "0":
		s += ", static"
	case "1":
		s += ", dynamic (cgo)"
	}
	return s
}

// shortTime turns 2026-10-25T14:03:11Z into "2026-10-25 14:03 UTC".
func shortTime(t string) string {
	if len(t) < 16 || t[10] != 'T' {
		return ""
	}
	return t[:10] + " " + t[11:16] + " UTC"
}

func versionString() string {
	b := buildInfo{Version: version, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Revision = s.Value
			case "vcs.time":
				b.Time = s.Value
			case "vcs.modified":
				b.Modified = s.Value == "true"
			case "CGO_ENABLED":
				b.CGO = s.Value
			}
		}
	}
	return formatVersion(b)
}
