package topology

import (
	"fmt"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// DeriveAllocations reconstructs the allocation records of a topology
// from the nodes and links themselves — the same rules the builder's
// restore applies. Used where a topology becomes desired state
// without going through the builder (rollback to a snapshot).
func DeriveAllocations(nodes []*model.Node, links []*model.Link) []model.Allocation {
	var allocs []model.Allocation

	rackSeen := make(map[string]bool)
	for _, n := range nodes {
		switch n.Spec.Role {
		case model.RoleServer:
			// Fixed well-known ASN, addressed inside the rack subnet.
		case model.RoleLeaf:
			allocs = append(allocs, model.Allocation{
				Pool: string(model.PoolLoopback), Value: n.Spec.Loopback.String(), Owner: n.Meta.Name,
			})

			owner := n.Spec.PodID + "/" + n.Spec.RackID
			if !rackSeen[owner] {
				rackSeen[owner] = true
				allocs = append(allocs,
					model.Allocation{Pool: "asn:" + string(model.RoleLeaf), Value: fmt.Sprint(n.Spec.ASN), Owner: owner},
					model.Allocation{Pool: string(model.PoolServerVlan), Value: n.Spec.VlanIP.Masked().String(), Owner: owner},
				)
			}
		default:
			allocs = append(allocs,
				model.Allocation{Pool: "asn:" + string(n.Spec.Role), Value: fmt.Sprint(n.Spec.ASN), Owner: n.Meta.Name},
				model.Allocation{Pool: string(model.PoolLoopback), Value: n.Spec.Loopback.String(), Owner: n.Meta.Name},
			)
		}
	}

	for _, l := range links {
		if l.Spec.Kind == model.LinkFabric {
			allocs = append(allocs, model.Allocation{
				Pool: string(model.PoolFabricP2P), Value: l.Spec.EndpointA.Address.Masked().String(), Owner: l.Meta.Name,
			})
		}
	}

	return allocs
}
