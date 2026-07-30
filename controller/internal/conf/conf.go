// Package conf holds the runtime configuration injected into the wire
// providers, mirroring the conf layer of the kratos-layout. The
// controller is configured by command-line flags (filled in cmd/), so
// the config is plain Go structs instead of a protobuf + config file.
package conf

// Server configures the transport servers.
type Server struct {
	HTTPAddr string // HTTP API listen address
	GRPCAddr string // gRPC listen address, "" disables gRPC
	RepoAddr string // package repository listen address, "" disables it
}

// Data configures the data layer: persistence and the runtime driver.
type Data struct {
	Dir       string // database and artifact directory
	Runtime   string // runtime driver: agent, noop or auto
	AgentAddr string // data plane agent gRPC address the runtime driver dials
	BinDir    string // dir with the builtin package binaries (trafficgen, capture), "" disables them
}
