VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BINARY := docker-machine-driver-hetzner
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build build-linux clean test

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/docker-machine-driver-hetzner

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)_linux_amd64 ./cmd/docker-machine-driver-hetzner
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)_linux_arm64 ./cmd/docker-machine-driver-hetzner

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY) $(BINARY)_linux_*

install: build
	cp $(BINARY) $(GOPATH)/bin/
