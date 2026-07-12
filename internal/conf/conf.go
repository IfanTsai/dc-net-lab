// Package conf holds the runtime configuration injected into the wire
// providers, mirroring the conf layer of the kratos-layout. The
// controller is configured by command-line flags (filled in cmd/), so
// the config is plain Go structs instead of a protobuf + config file.
package conf

// Server configures the transport servers.
type Server struct {
	HTTPAddr string // HTTP listen address
	GRPCAddr string // gRPC listen address, "" disables gRPC
	WebDir   string // built web UI directory, "" disables serving it
	RepoAddr string // package repository listen address, "" disables it
}

// Data configures the data layer: persistence and the runtime driver.
type Data struct {
	Dir     string // database and artifact directory
	Runtime string // runtime driver: containerlab, noop or auto
	BinDir  string // host dir with node-agent/trafficgen binaries, "" disables the agent
}
