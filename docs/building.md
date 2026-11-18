# Building and installing go-at

Everything beyond the three lines of `make install` in the README: static builds, prebuilt binaries from GitHub, copying to a machine with no Go, and restarting the daemon after an upgrade.

[← back to the README](../README.md)

## With make

    make test                             # go vet + unit and end-to-end tests
    make install                          # ~/.local/bin/{go-at,go-atq,go-atrm,go-batch,go-atd}
    make static                           # builds against static libraries for portability
    sudo make install PREFIX=/usr/local   # installs in /usr/local/bin (0755)

Needs Go **1.24.7 or newer** to build (the `go` line in `go.mod`; no older release is supported). Prefer a currently supported Go release for security fixes. For a fully static binary that needs no libc (handy on seedboxes and minimal containers) use `make static` instead of `make`, then `make install`; cross-compile with e.g. `GOARCH=arm64 make static` (`GOARCH`/`GOARM` are read by Go itself; the Makefile has no architecture option of its own). A plain `go build` links libc dynamically, because Go's `os/user` uses cgo when it is available; check with `ldd go-at`. Make sure `~/.local/bin` is on your `PATH`. If you want the classic names, symlink them yourself (`ln -s go-at ~/.local/bin/at`); the binary answers to either spelling.

## Building without make, or on a machine that has no Go

Build where Go is installed, copy the one file to the machine that runs it
(a seedbox, say); that machine needs no Go. A static build has no libc
dependency, so the two machines only have to share a CPU architecture, not
library versions:

    # on the build machine, in the clone (same command as `make static`)
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o go-at .
    #   other CPU:  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ...   (see: uname -m)

    scp go-at seedbox:.local/bin/go-at.new
    ssh seedbox 'cd ~/.local/bin && mv -f go-at.new go-at \
        && for n in go-atq go-atrm go-batch go-atd; do ln -sf go-at $n; done && go-at --version'

**Or skip building: download a prebuilt static binary** (linux amd64 and arm64).
Every push to a branch publishes a prerelease `dev-<branch>`, and a `v*` tag
publishes a release (only if the tests pass), each with a `SHA256SUMS` file:

    # a branch you want to try (the binary is replaced on every push to it)
    curl -fL -o go-at.new https://github.com/iconoclasthero/go-at/releases/download/dev-BRANCH/go-at-linux-amd64
    # the latest tagged release
    curl -fL -o go-at.new https://github.com/iconoclasthero/go-at/releases/latest/download/go-at-linux-amd64

then `mv` it into place as below. `go-at --version` names the branch or tag and
the commit. When a branch is done, `gh release delete dev-BRANCH --cleanup-tag`.

Copy to a new name and `mv` it into place: copying over a running binary fails
with "text file busy", while a rename is fine. A running `go-atd` keeps the old
code until it is restarted (supervisord: `supervisorctl restart <program>`;
systemd: `systemctl --user restart atd`).

`go-at --version` prints the commit the binary was built from, `+modified` if
the tree had uncommitted changes, the build time, and `static` or `dynamic (cgo)`.
The commit is recorded by `go build` itself, so a build from a git checkout
needs nothing extra; from a tarball it says `unknown commit`. A release can name
itself with `-ldflags "-X main.version=v1.2.3"`.
