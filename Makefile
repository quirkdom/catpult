# catpult build tooling.
#
# Common targets:
#   make build                 host binary at ./catpult
#   make build-all             release binaries for every target in PLATFORMS, into dist/
#   make build-one PLATFORM=windows/amd64
#   make test vet fmt check clean help

BINARY  := catpult
PKG     := ./cmd/catpult
DIST    := dist
GO      ?= go

# Stamp the binary; override with VERSION=... (make VERSION=v1.2.3 build).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# catpult has no cgo dependency; keep cross builds pure Go and static.
export CGO_ENABLED := 0

# Tailcat build tags for space savings (matches tailcat's build-tags.txt).
# Includes base tags (netgo, osusergo, omitidna, omitpemdecrypt) and omits
# all tailscale.com features tailcat doesn't need.
TAILCAT_TAGS ?= $(shell (cd ~/Developer/tailcat && go run ./internal/buildtags/printtags) 2>/dev/null || cat ~/Developer/tailcat/build-tags.txt 2>/dev/null)

BUILD_FLAGS := -trimpath -tags '$(TAILCAT_TAGS)'
LDFLAGS     := -s -w -X main.version=$(VERSION)

PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

GO_SOURCES := $(shell find . -type f -name '*.go' -not -path "./vendor/*") go.mod go.sum

.PHONY: all build-all build-one install test vet fmt check package checksums clean deep-clean help print-env

all: build

## build: compile for this host into ./$(BINARY)
build: $(BINARY)

$(BINARY): $(GO_SOURCES)
	$(GO) build $(BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

## build-all: cross-compile every platform in PLATFORMS into $(DIST)/
build-all:
	@mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=$(DIST)/$(BINARY)-$$os-$$arch; \
		if [ "$$os" = windows ]; then out=$$out.exe; fi; \
		printf ">> %s/%s -> " "$$os" "$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO) build $(BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o "$$out" $(PKG); \
		echo "$$out"; \
	done

## build-one: cross-compile a single platform, e.g. make build-one PLATFORM=linux/arm64
build-one:
	@mkdir -p $(DIST)
	@set -e; os=$${PLATFORM%/*}; arch=$${PLATFORM#*/}; \
		out=$(DIST)/$(BINARY)-$$os-$$arch; \
		if [ "$$os" = windows ]; then out=$$out.exe; fi; \
		printf ">> %s/%s -> " "$$os" "$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO) build $(BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o "$$out" $(PKG); \
		echo "$$out"

## install: go install onto GOBIN/GOPATH
install:
	$(GO) install $(BUILD_FLAGS) -ldflags '$(LDFLAGS)' $(PKG)

## test: run the test suite
test:
	$(GO) test ./...

## vet: run go vet
vet:
	$(GO) vet ./...

## fmt: gofmt the tree in place
fmt:
	$(GO) fmt ./...

## check: formatting gate, vet, tests, and the full cross-compile matrix
check:
	@files=$$(find . -type f -name '*.go' -not -path "./vendor/*"); \
	if [ -n "$$files" ]; then \
		out=$$(gofmt -l $$files); \
		if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi; \
	fi
	$(GO) vet ./...
	$(GO) test ./...
	@$(MAKE) build-all

## package: package cross-compiled binaries into archives
package: build-all
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		src=$(DIST)/$(BINARY)-$$os-$$arch; \
		stage=$(DIST)/stage-$$os-$$arch; \
		mkdir -p "$$stage"; \
		if [ "$$os" = windows ]; then \
			cp "$$src.exe" "$$stage/$(BINARY).exe"; \
			cp README.md "$$stage/"; \
			(cd "$$stage" && zip -q -r "../$(BINARY)-$$os-$$arch.zip" .); \
		else \
			cp "$$src" "$$stage/$(BINARY)"; \
			cp README.md "$$stage/"; \
			tar -czf "$(DIST)/$(BINARY)-$$os-$$arch.tar.gz" -C "$$stage" .; \
		fi; \
		rm -rf "$$stage"; \
		echo ">> packaged $(DIST)/$(BINARY)-$$os-$$arch"; \
	done

## checksums: generate sha256 checksums in $(DIST)/
checksums:
	@cd $(DIST) && rm -f checksums.txt && (sha256sum $(BINARY)* 2>/dev/null || shasum -a 256 $(BINARY)*) > checksums.txt
	@echo ">> generated $(DIST)/checksums.txt"

## clean: remove the build outputs this Makefile produces
clean:
	rm -rf $(DIST) $(BINARY)

## deep-clean: clean build artifacts, bin/ outputs, and purge Go build & test caches
deep-clean: clean
	rm -rf bin *.out coverage.html
	$(GO) clean -cache -testcache

help:
	@echo "catpult make targets (VERSION=$(VERSION)):"
	@echo "  build       host binary -> ./$(BINARY)"
	@echo "  build-all   cross-compile PLATFORMS -> $(DIST)/"
	@echo "  build-one   make build-one PLATFORM=linux/arm64"
	@echo "  install     go install to GOBIN/GOPATH"
	@echo "  test        go test ./..."
	@echo "  vet         go vet ./..."
	@echo "  fmt         go fmt ./..."
	@echo "  check       gofmt gate + vet + test + build-all"
	@echo "  package     package binaries into archives (.tar.gz / .zip) in $(DIST)/"
	@echo "  checksums   generate sha256 checksums in $(DIST)/checksums.txt"
	@echo "  clean       remove $(DIST)/ and ./$(BINARY)"
	@echo "  deep-clean  clean build outputs + purge Go build & test caches"
	@echo "  help        this message"
	@echo "Use -B / --always-make to force run a target"

print-env:
	@$(foreach v, $(.VARIABLES), $(if $(filter file,$(origin $(v))), $(info $(v)=$($(v)))))
	@true
