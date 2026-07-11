// Package server assembles the Kratos transport servers: the HTTP
// server carrying the protobuf-defined REST API (plus the built web
// UI and the terminal WebSocket) and the gRPC server exposing the
// same service.
package server

import "github.com/google/wire"

// ProviderSet is the server-layer providers. The TerminalOpener
// binding lives in cmd/controller/wire.go next to its provider.
var ProviderSet = wire.NewSet(NewHTTPServer, NewGRPCServer)
