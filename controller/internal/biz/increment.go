package biz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ifantsai/dcnetlab/controller/internal/compiler/containerlab"
	"github.com/ifantsai/dcnetlab/controller/internal/topology"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// buildIncrement composes the incremental deploy artifact from the
// diff between the deployed base and the new desired topology. Config
// changes on surviving nodes are detected by comparing the rendered
// frr.conf between the two generation directories — exact, and free
// of any duplicated knowledge about which model change affects which
// config; an unreadable old config falls back to a reload, which is
// safe because frr-reload only applies actual deltas.
func buildIncrement(labName string, base *topology.Base, nodes []*model.Node, links []*model.Link,
	oldGenDir, newGenDir string) (*runtime.Increment, error) {
	baseNode := make(map[string]bool, len(base.Nodes))
	for _, n := range base.Nodes {
		baseNode[n.Meta.Name] = true
	}

	baseLink := make(map[string]bool, len(base.Links))
	for _, l := range base.Links {
		baseLink[l.Meta.Name] = true
	}

	newNode := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		newNode[n.Meta.Name] = true
	}

	newLink := make(map[string]bool, len(links))
	for _, l := range links {
		newLink[l.Meta.Name] = true
	}

	inc := &runtime.Increment{LabName: labName}
	for _, n := range nodes {
		if !baseNode[n.Meta.Name] {
			inc.AddNodes = append(inc.AddNodes, n.Meta.Name)
		}
	}

	for _, l := range links {
		if baseLink[l.Meta.Name] {
			continue
		}

		a, z := l.Spec.EndpointA, l.Spec.EndpointB
		inc.AddLinks = append(inc.AddLinks, runtime.IncrementLink{
			A: runtime.IncrementEndpoint{Node: a.NodeName, Interface: a.Interface},
			B: runtime.IncrementEndpoint{Node: z.NodeName, Interface: z.Interface},
		})

		// A new server behind a surviving leaf: the leaf's bridge got
		// its port list at its own creation, so the new access port
		// must be attached as a delta command.
		if l.Spec.Kind == model.LinkServerAccess && baseNode[a.NodeName] {
			if inc.NodeExec == nil {
				inc.NodeExec = make(map[string][]string)
			}

			inc.NodeExec[a.NodeName] = append(inc.NodeExec[a.NodeName],
				containerlab.AccessPortAttach(a.Interface, l.Spec.VlanID)...)
		}
	}

	for _, l := range base.Links {
		if newLink[l.Meta.Name] {
			continue
		}

		// Removed links vanish with the removed container of one of
		// their endpoints; a removed link between two survivors has no
		// container removal to take it down and would need an explicit
		// teardown this platform's scale operations never produce.
		if newNode[l.Spec.EndpointA.NodeName] && newNode[l.Spec.EndpointB.NodeName] {
			return nil, fmt.Errorf("link %s is removed but both endpoints survive; not supported by incremental deploy", l.Meta.Name)
		}
	}

	for _, n := range base.Nodes {
		if !newNode[n.Meta.Name] {
			inc.RemoveNodes = append(inc.RemoveNodes, n.Meta.Name)
		}
	}

	for _, n := range nodes {
		if !baseNode[n.Meta.Name] {
			continue
		}

		rel := filepath.Join("configs", n.Meta.Name, "frr.conf")
		newConf, err := os.ReadFile(filepath.Join(newGenDir, rel))
		if err != nil {
			return nil, fmt.Errorf("read rendered config of %s: %w", n.Meta.Name, err)
		}

		oldConf, err := os.ReadFile(filepath.Join(oldGenDir, rel))
		if err != nil || !bytes.Equal(oldConf, newConf) {
			inc.ReloadNodes = append(inc.ReloadNodes, n.Meta.Name)
		}
	}

	return inc, nil
}

// restoreScope returns the nodes whose containers a deploy created
// fresh: every node on a from-scratch deploy, only the added ones on
// an incremental apply. Program restore and capture-tool delivery run
// against this scope — surviving servers kept their agents, packages
// and running programs.
func restoreScope(baseSnap *DesiredStateSnapshot, nodes []*model.Node) []*model.Node {
	if baseSnap == nil {
		return nodes
	}

	inBase := make(map[string]bool, len(baseSnap.Nodes))
	for _, n := range baseSnap.Nodes {
		inBase[n.Meta.Name] = true
	}

	added := make([]*model.Node, 0, len(nodes))
	for _, n := range nodes {
		if !inBase[n.Meta.Name] {
			added = append(added, n)
		}
	}

	return added
}

// writeIncrement persists the increment artifact next to the topology
// file so it travels to the agent with the rest of the generation.
func writeIncrement(inc *runtime.Increment, genDir string) error {
	raw, err := json.MarshalIndent(inc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal increment: %w", err)
	}

	if err := os.WriteFile(filepath.Join(genDir, runtime.IncrementFileName), raw, 0o644); err != nil {
		return fmt.Errorf("write increment: %w", err)
	}

	return nil
}
