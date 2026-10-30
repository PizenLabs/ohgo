# ohgo build

MODULE := github.com/PizenLabs/ohgo
BIN := ohgo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt lint race check bench install uninstall clean docker snapshot release

# Install location. Defaults to ~/.local (first on a typical PATH, no sudo);
# override for system installs: `sudo make install PREFIX=/usr/local`.
# DESTDIR staging is supported for packagers.
PREFIX ?= $(HOME)/.local
DESTDIR ?=
BINDIR ?= $(PREFIX)/bin

# The release matrix. The workflow runs the same script once per entry, so a
# local `make release` and a tagged release produce identical artifacts.
RELEASE_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# release builds versioned static tarballs for the supported platforms plus
# checksums. Everything is CGO_ENABLED=0: a release artifact must run anywhere
# without a toolchain or shared libraries.
release:
	rm -rf dist && mkdir -p dist
	for pair in $(RELEASE_TARGETS); do \
		GOOS=$${pair%/*} GOARCH=$${pair#*/} VERSION="$(VERSION)" \
			./scripts/release-target.sh || exit 1; \
	done
	cd dist && if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum *.tar.gz; \
	else \
		shasum -a 256 *.tar.gz; \
	fi > sha256sums.txt && cat sha256sums.txt

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/ohgo

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l .
	@test -z "$$(gofmt -l .)" || (echo "gofmt: files need formatting" && exit 1)

lint:
	golangci-lint run ./...

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
	docker build --build-arg VERSION=$(VERSION) -t ohgo:$(VERSION) .

snapshot:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)-$(VERSION) ./cmd/ohgo
