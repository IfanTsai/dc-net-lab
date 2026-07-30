GO ?= go
# buf and the protoc plugins are installed into GOPATH/bin by `make init`.
TOOL_PATH := $(shell $(GO) env GOPATH)/bin:$(PATH)

.PHONY: all init api wire build test vet lint run up dev down status orb-setup web-install web-dev web-build images edge-image clean

all: build test

# Install the protobuf/Kratos toolchain (buf, protoc-gen-go,
# protoc-gen-go-grpc, protoc-gen-go-http, kratos CLI).
init:
	scripts/init-tools.sh

# Regenerate Go code under pb/ from the protobuf API definition in api/.
api:
	PATH="$(TOOL_PATH)" buf generate

# Regenerate controller/cmd/controller/wire_gen.go after changing providers.
wire:
	PATH="$(TOOL_PATH)" wire ./controller/cmd/controller

# node-agent, node-cli, trafficgen and capture are static (CGO off):
# they run inside the Alpine-based FRR containers — baked into the
# node images (make images) or delivered as builtin packages.
build:
	$(GO) build ./...
	$(GO) build -o bin/dcnetlab-controller ./controller/cmd/controller
	$(GO) build -o bin/dcnetlab-agent ./agent/cmd/agent
	$(GO) build -o bin/dcnetlab-web ./web/server
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-node-agent ./nodeapps/cmd/node-agent
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-node-cli ./nodeapps/cmd/node-cli
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-trafficgen ./nodeapps/cmd/trafficgen
	CGO_ENABLED=0 $(GO) build -o bin/dcnetlab-capture ./nodeapps/cmd/capture

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Static analysis per docs/golang-style.md; zero findings required.
# golangci-lint covers the mechanical rules, check-style.py the
# blank-line semantics it cannot express.
lint:
	PATH="$(TOOL_PATH)" golangci-lint run
	python3 scripts/check-style.py

# Build every node image (dcnetlab/frr, dcnetlab/server, frr-edge);
# on macOS the launcher forwards the builds into the OrbStack machine
# so they land in the daemon the agent deploys with.
images: build
	scripts/dcnetlab images

# Build only the FRR + iptables edge image used by dcedge/external
# when a lab enables internet access.
edge-image:
	scripts/dcnetlab edge-image

# Regenerate compiler golden files after intentional template changes.
golden:
	$(GO) test ./controller/internal/compiler/... -update

run: build
	./bin/dcnetlab-controller --data-dir data

# One-command start/stop of the three processes (agent, controller,
# web); "dev" serves the UI with hot reload through the web port.
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
