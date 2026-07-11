#!/usr/bin/env bash
# init-tools.sh installs everything needed to regenerate the protobuf
# API and build the Kratos-based controller. Idempotent: safe to re-run.
#
# Requirements installed by this script (all via `go install`):
#   - buf                 protobuf compiler + dependency manager
#   - protoc-gen-go       protobuf message codegen
#   - protoc-gen-go-grpc  gRPC service codegen
#   - protoc-gen-go-http  Kratos HTTP service codegen
#   - kratos              Kratos CLI (proto scaffolding, optional helper)
#   - wire                compile-time dependency injection codegen
#
# Prerequisite assumed present: go >= 1.25.
set -euo pipefail

# Pinned tool versions; bump deliberately, together with go.mod.
BUF_VERSION="v1.71.0"
PROTOC_GEN_GO_VERSION="v1.36.6"
PROTOC_GEN_GO_GRPC_VERSION="v1.5.1"
# The kratos cmd/* submodules carry no version tags, only pseudo-versions;
# this one corresponds to the v2.9.x line used in go.mod.
KRATOS_CMD_VERSION="v2.0.0-20260404020628-f149714c1d54"
WIRE_VERSION="v0.7.0"
GOLANGCI_LINT_VERSION="v2.12.2"

GOBIN="$(go env GOPATH)/bin"
export PATH="$GOBIN:$PATH"

log() { printf '\033[1;34m[init-tools]\033[0m %s\n' "$*"; }

log "installing buf ${BUF_VERSION}"
go install "github.com/bufbuild/buf/cmd/buf@${BUF_VERSION}"

log "installing protoc-gen-go ${PROTOC_GEN_GO_VERSION}"
go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"

log "installing protoc-gen-go-grpc ${PROTOC_GEN_GO_GRPC_VERSION}"
go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"

log "installing protoc-gen-go-http ${KRATOS_CMD_VERSION} (Kratos)"
go install "github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v2@${KRATOS_CMD_VERSION}"

log "installing kratos CLI ${KRATOS_CMD_VERSION}"
go install "github.com/go-kratos/kratos/cmd/kratos/v2@${KRATOS_CMD_VERSION}"

# wire v0.7.0 pins an x/tools too old to load go1.25 packages, so build
# it in a throwaway module with x/tools upgraded instead of a plain
# `go install wire@version`.
log "installing wire ${WIRE_VERSION} (with upgraded x/tools)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
(
  cd "$tmp"
  go mod init wire-install >/dev/null
  go get "github.com/google/wire/cmd/wire@${WIRE_VERSION}" >/dev/null 2>&1
  go get golang.org/x/tools@latest >/dev/null 2>&1
  go install github.com/google/wire/cmd/wire
)

log "installing golangci-lint ${GOLANGCI_LINT_VERSION}"
go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"

log "done. Tools in $GOBIN:"
for t in buf protoc-gen-go protoc-gen-go-grpc protoc-gen-go-http kratos wire golangci-lint; do
  printf '  %-22s %s\n' "$t" "$(command -v "$t" || echo MISSING)"
done
log "regenerate the API with: make api"
