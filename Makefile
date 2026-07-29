GO ?= go
# buf and the protoc plugins are installed into GOPATH/bin by `make init`.
TOOL_PATH := $(shell $(GO) env GOPATH)/bin:$(PATH)

.PHONY: all init api wire build test vet lint run up dev down status orb-setup web-install web-dev web-build edge-image clean

all: build test

# Install the protobuf/Kratos toolchain (buf, protoc-gen-go,
# protoc-gen-go-grpc, protoc-gen-go-http, kratos CLI).
init:
	scripts/init-tools.sh

# Regenerate Go code under pb/ from the protobuf API definition in api/.
api:
	PATH="$(TOOL_PATH)" buf generate

# Regenerate cmd/controller/wire_gen.go after changing providers.
wire:
	PATH="$(TOOL_PATH)" wire ./cmd/controller

# node-agent, node-cli and trafficgen are static (CGO off): they run
# inside the Alpine-based FRR containers via a bind mount from bin/.
build:
	$(GO) build ./...
	$(GO) build -o bin/dcnetlab-controller ./cmd/controller
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-node-agent ./serverapps/node-agent
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-node-cli ./serverapps/node-cli
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-trafficgen ./serverapps/trafficgen
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-capture ./serverapps/capture

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Static analysis per doc/golang-style.md; zero findings required.
# golangci-lint covers the mechanical rules, check-style.py the
# blank-line semantics it cannot express.
lint:
	PATH="$(TOOL_PATH)" golangci-lint run
	python3 scripts/check-style.py

# Build the FRR + iptables image used by dcedge/external when a lab
# enables internet access; on macOS the launcher forwards the build
# into the OrbStack machine so it lands in the controller's daemon.
edge-image:
	scripts/dcnetlab edge-image

# Regenerate compiler golden files after intentional template changes.
golden:
	$(GO) test ./internal/compiler/... -update

run: build
	./bin/dcnetlab-controller --data-dir data

# One-command start/stop of backend + web UI; "dev" serves the UI
# with hot reload through the controller port.
up:
	scripts/dcnetlab up

dev:
	scripts/dcnetlab up --dev

down:
	scripts/dcnetlab down

status:
	scripts/dcnetlab status

# One-time OrbStack machine setup on macOS: create the Linux machine
# and install docker/containerlab/go/node so "up" deploys for real.
orb-setup:
	scripts/orb-setup

web-install:
	cd web && npm install

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

clean:
	rm -rf bin data web/dist .run
