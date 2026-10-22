PREFIX ?= $(HOME)/.local
# go-at is the program; the others are names it answers to
LINKS := go-atq go-atrm go-batch go-atd

all: go-at

go-at: *.go
	go build -o go-at .

test:
	go vet ./... && go test ./...

# one binary, five names (it dispatches on the name it is called by)
install: go-at
	install -d $(PREFIX)/bin
	install -m755 go-at $(PREFIX)/bin/go-at
	for n in $(LINKS); do ln -sf go-at $(PREFIX)/bin/$$n; done

install-service:
	install -d $(HOME)/.config/systemd/user
	install -m644 atd.service $(HOME)/.config/systemd/user/atd.service
	systemctl --user daemon-reload
	systemctl --user enable --now atd

clean:
	rm -f go-at

.PHONY: all test install install-service clean
