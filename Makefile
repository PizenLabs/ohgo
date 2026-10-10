# ohgo build

MODULE := github.com/PizenLabs/ohgo
BIN := ohgo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt race check bench install uninstall clean docker snapshot release

# Install location. Defaults to ~/.local (first on a typical PATH, no sudo);
# override for system installs: `sudo make install PREFIX=/usr/local`.
# DESTDIR staging is supported for packagers.
PREFIX ?= $(HOME)/.local
DESTDIR ?=
BINDIR ?= $(PREFIX)/bin

# release builds versioned static tarballs for the supported platforms plus
# checksums. Everything is CGO_ENABLED=0: a release artifact must run anywhere
# without a toolchain or shared libraries.
release:
	rm -rf dist && mkdir -p dist
	for pair in "linux/amd64" "linux/arm64" "darwin/amd64" "darwin/arm64"; do \
		os=$${pair%/*}; arch=$${pair#*/}; \
		dir="dist/ohgo-$(VERSION)-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o "$$dir/ohgo" ./cmd/ohgo || exit 1; \
		cp README.md LICENSE "$$dir/"; \
		tar -czf "$$dir.tar.gz" -C dist "$$(basename $$dir)" || exit 1; \
		rm -rf "$$dir"; \
	done
	cd dist && sha256sum *.tar.gz > sha256sums.txt && cat sha256sums.txt

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/ohgo

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .
	@test -z "$$(gofmt -l .)" || (echo "gofmt: files need formatting" && exit 1)

race:
	go test -race ./...

check: build vet fmt test

bench:
	go test -run=NONE -bench=. -benchmem ./tools/ ./agent/ ./ai/ 2>&1 | head -60

install: build
	mkdir -p $(DESTDIR)$(BINDIR)
	install -m 755 $(BIN) $(DESTDIR)$(BINDIR)/ohgo
	@echo "installed ohgo $(VERSION) to $(DESTDIR)$(BINDIR)/ohgo"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/ohgo

clean:
	rm -f $(BIN) coverage.* *.out *.test

docker:
	docker build -t ohgo:$(VERSION) .

snapshot:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)-$(VERSION) ./cmd/ohgo
