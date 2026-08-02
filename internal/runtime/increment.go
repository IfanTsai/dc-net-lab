package runtime

// IncrementFileName is the artifact file describing an incremental
// deploy, written next to the topology file. The controller composes
// it from the diff between the deployed generation and the new one;
// the agent executes it against the running lab.
const IncrementFileName = "increment.json"

// Increment describes a topology change applied to a running lab
// without touching unchanged containers: new nodes are created and
// wired, surviving nodes get delta commands and config reloads, and
// removed nodes are destroyed (their veth pairs die with them).
type Increment struct {
	LabName string `json:"labName"`
	// AddNodes are node names to create; their definitions (image,
	// binds, exec) come from the topology file in the same directory.
	// Order follows the topology build order.
	AddNodes []string `json:"addNodes,omitempty"`
	// AddLinks are veth pairs to create once all new containers run;
	// either endpoint may be a surviving node.
	AddLinks []IncrementLink `json:"addLinks,omitempty"`
	// NodeExec are delta commands on surviving nodes, e.g. attaching a
	// new access port to a leaf's bridge. New nodes run their full
	// exec list from the topology file instead.
	NodeExec map[string][]string `json:"nodeExec,omitempty"`
	// ReloadNodes are surviving FRR nodes whose rendered config
	// changed; the agent re-applies configs/<node>/frr.conf against
	// the running daemons (frr-reload), so neighbors towards removed
	// nodes are deconfigured before their containers disappear.
	ReloadNodes []string `json:"reloadNodes,omitempty"`
	// RemoveNodes are node names whose containers are destroyed, after
	// reloads so surviving neighbors part gracefully.
	RemoveNodes []string `json:"removeNodes,omitempty"`
}

// IncrementLink is one veth pair between two lab containers.
type IncrementLink struct {
	A IncrementEndpoint `json:"a"`
	B IncrementEndpoint `json:"b"`
}

// IncrementEndpoint is one side of an incremental link.
type IncrementEndpoint struct {
	Node      string `json:"node"`
	Interface string `json:"iface"`
}

// Empty reports whether the increment changes nothing at runtime.
func (inc *Increment) Empty() bool {
	return len(inc.AddNodes) == 0 && len(inc.AddLinks) == 0 && len(inc.NodeExec) == 0 &&
		len(inc.ReloadNodes) == 0 && len(inc.RemoveNodes) == 0
}
