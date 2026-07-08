GO ?= go
GOFLAGS ?=
DIST_DIR ?= dist

.PHONY: build test lint vulncheck clean

build:
	mkdir -p $(DIST_DIR)
	$(GO) build $(GOFLAGS) -o $(DIST_DIR)/asc-sender ./cmd/asc-sender
	$(GO) build $(GOFLAGS) -o $(DIST_DIR)/asc-receive ./cmd/asc-receive
	$(GO) build $(GOFLAGS) -o $(DIST_DIR)/asc-hub ./cmd/asc-hub

test:
	$(GO) test $(GOFLAGS) ./...

lint:
	$(GO) vet $(GOFLAGS) ./...

vulncheck:
	govulncheck ./...

clean:
	rm -rf $(DIST_DIR)
