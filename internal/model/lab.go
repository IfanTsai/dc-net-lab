package model

// ProfileName identifies a predefined topology profile.
type ProfileName string

const (
	ProfileMicro    ProfileName = "micro"
	ProfileStandard ProfileName = "standard"
	ProfileCustom   ProfileName = "custom"
)

// AddressPoolName identifies a managed address pool.
type AddressPoolName string

const (
	PoolManagement AddressPoolName = "management"
	PoolLoopback   AddressPoolName = "loopback"
	PoolFabricP2P  AddressPoolName = "fabricP2P"
	// PoolServerVlan hands out one subnet per rack: the shared VLAN
	// where both MLAG leaves and the rack's servers live.
	PoolServerVlan AddressPoolName = "serverVlan"
	PoolVTEP       AddressPoolName = "vtep"
	PoolWorkload   AddressPoolName = "workload"
	PoolPublic     AddressPoolName = "public"
)

// AddressPool declares one CIDR pool managed by the IPAM allocator.
type AddressPool struct {
	Name             AddressPoolName `json:"name" yaml:"name"`
	CIDR             string          `json:"cidr" yaml:"cidr"`
	AllocationPrefix int             `json:"allocationPrefix" yaml:"allocationPrefix"`
}

// ASNRange declares a contiguous ASN range for one device role.
type ASNRange struct {
	Role  NodeRole `json:"role" yaml:"role"`
	Start uint32   `json:"start" yaml:"start"`
	End   uint32   `json:"end" yaml:"end"`
}

// Lab is the top-level resource holding one simulated data center.
type Lab struct {
	Meta   ResourceMeta `json:"meta"`
	Spec   LabSpec      `json:"spec"`
	Status LabStatus    `json:"status"`
}

// LabSpec is the desired state of a lab.
type LabSpec struct {
	Profile  ProfileName   `json:"profile"`
	Topology TopologySpec  `json:"topology"`
	Pools    []AddressPool `json:"pools"`
	ASNs     []ASNRange    `json:"asns"`
}

// TopologySpec describes the desired fabric shape.
type TopologySpec struct {
	ExternalRouters int       `json:"externalRouters"`
	DCEdges         int       `json:"dcEdges"`
	SuperSpines     int       `json:"superSpines"`
	Pods            []PodSpec `json:"pods"`
	// InternetAccess connects the simulated fabric to the real
	// internet: dc-edges NAT outbound traffic at the DC boundary and
	// externals attach to a host-side WAN network and originate a
	// default route into the fabric. Off means an air-gapped DC.
	InternetAccess bool `json:"internetAccess"`
}

// PodSpec describes one pod inside the fabric. Leaves come in racks:
// each rack is one MLAG pair of leaf switches plus its servers.
type PodSpec struct {
	Name           string `json:"name"`
	Spines         int    `json:"spines"`
	Racks          int    `json:"racks"`
	ServersPerRack int    `json:"serversPerRack"`
}

// LabStatus is the observed state of a lab.
type LabStatus struct {
	NodeCount int `json:"nodeCount"`
	LinkCount int `json:"linkCount"`
}
