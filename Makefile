PREFIX ?= $(HOME)/.local
BIN := at atq atrm batch atd

all: go-at

go-at: *.go
	go build -o go-at .

test:
	go vet ./... && go test ./...

# one binary, five names (it dispatches on argv[0])
install: go-at
	install -d $(PREFIX)/bin
	install -m755 go-at $(PREFIX)/bin/go-at
	for n in $(BIN); do ln -sf go-at $(PREFIX)/bin/$$n; done

install-service:
	install -d $(HOME)/.config/systemd/user
	install -m644 atd.service $(HOME)/.config/systemd/user/atd.service
	systemctl --user daemon-reload
	systemctl --user enable --now atd

clean:
	rm -f go-at

.PHONY: all test install install-service clean
