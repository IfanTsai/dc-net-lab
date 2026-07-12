package model

// PackageFormatTarGz is the only supported archive format for now;
// the field exists so deb/OCI packages can join later.
const PackageFormatTarGz = "targz"

// Builtin trafficgen package identity. The controller registers the
// bundled trafficgen binary under this name at startup; the version is
// bumped with the trafficgen itself, and a changed binary behind the
// same version replaces the stored payload (dev builds).
const (
	BuiltinPackageName       = "trafficgen"
	BuiltinPackageVersion    = "0.1.0"
	BuiltinPackageEntrypoint = "dcnetlab-trafficgen"
)

// Package is one versioned program artifact in the controller's
// repository: a tar.gz archive with a manifest.json and the
// executables. Meta.Name is the package name; one Package resource
// exists per (name, version).
type Package struct {
	Meta   ResourceMeta  `json:"meta"`
	Spec   PackageSpec   `json:"spec"`
	Status PackageStatus `json:"status"`
}

// PackageSpec describes the artifact as declared by its manifest.
type PackageSpec struct {
	Version     string `json:"version"`
	Format      string `json:"format"`
	Entrypoint  string `json:"entrypoint"`
	Description string `json:"description,omitempty"`
}

// PackageStatus is what the controller derived from the payload.
type PackageStatus struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	Builtin   bool   `json:"builtin"`
}

// InstalledPackage is one package version present in a server's
// local store, as reported by its agent.
type InstalledPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
