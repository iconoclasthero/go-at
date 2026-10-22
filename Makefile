PREFIX ?= $(HOME)/.local
BIN := at atq atrm batch atd

all: at-go

at-go: *.go
	go build -o at-go .

test:
	go vet ./... && go test ./...

# one binary, five names (it dispatches on argv[0])
install: at-go
	install -d $(PREFIX)/bin
	install -m755 at-go $(PREFIX)/bin/at-go
	for n in $(BIN); do ln -sf at-go $(PREFIX)/bin/$$n; done

install-service:
	install -d $(HOME)/.config/systemd/user
	install -m644 atd.service $(HOME)/.config/systemd/user/atd.service
	systemctl --user daemon-reload
	systemctl --user enable --now atd

clean:
	rm -f at-go
