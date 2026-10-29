GO ?= go
GOFLAGS ?=
GOVULNCHECK_VERSION ?= v1.9.0
DIST_DIR ?= dist
VERSION ?= $(shell git describe --always --dirty --abbrev=12 2>/dev/null || printf dev)
BUILD_FLAGS = -trimpath -ldflags "-X main.version=$(VERSION)"

.PHONY: build test fmtcheck lint vulncheck clean

build:
	mkdir -p $(DIST_DIR)
	$(GO) build $(GOFLAGS) $(BUILD_FLAGS) -o $(DIST_DIR)/asc-sender ./cmd/asc-sender
	$(GO) build $(GOFLAGS) $(BUILD_FLAGS) -o $(DIST_DIR)/asc-receive ./cmd/asc-receive
	$(GO) build $(GOFLAGS) $(BUILD_FLAGS) -o $(DIST_DIR)/asc-hub ./cmd/asc-hub

test:
	$(GO) test $(GOFLAGS) ./...

fmtcheck:
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

lint: fmtcheck
	$(GO) vet $(GOFLAGS) ./...

vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

clean:
	rm -rf $(DIST_DIR)
