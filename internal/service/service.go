// Package service implements the protobuf-defined DCNetLab API on top
// of the biz layer. It converts between protobuf messages and the
// internal resource model and maps errors to Kratos error codes; it
// never touches Docker, Containerlab or the Linux network directly.
package service

import "github.com/google/wire"

// ProviderSet is the service-layer providers.
var ProviderSet = wire.NewSet(NewDCNetLabService)
