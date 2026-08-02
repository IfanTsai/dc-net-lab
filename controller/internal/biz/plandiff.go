package biz

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ifantsai/dcnetlab/controller/internal/topology"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// carryOverIdentity re-attaches the stored identity and observed state
// to a rebuilt topology: a rebuilt node or link whose name the desired
// store already has keeps that row's Meta (ID, phase, generations) and
// Status, so a plan never re-identifies resources that are alive —
// programs keep pointing at valid server IDs and the UI keeps its
// selection across plans. The freshly built spec always wins.
func carryOverIdentity(res *topology.Result, nodes []*model.Node, links []*model.Link) {
	nodeByName := make(map[string]*model.Node, len(nodes))
	for _, n := range nodes {
		nodeByName[n.Meta.Name] = n
	}

	linkByName := make(map[string]*model.Link, len(links))
	for _, l := range links {
		linkByName[l.Meta.Name] = l
	}

	idByName := make(map[string]string, len(res.Nodes))
	for _, n := range res.Nodes {
		if cur, ok := nodeByName[n.Meta.Name]; ok {
			n.Meta = cur.Meta
			n.Status = cur.Status
		}

		idByName[n.Meta.Name] = n.Meta.ID
	}

	for _, l := range res.Links {
		if cur, ok := linkByName[l.Meta.Name]; ok {
			l.Meta = cur.Meta
			l.Status = cur.Status
		}

		// Endpoint node IDs must follow the carried-over node identity.
		l.Spec.EndpointA.NodeID = idByName[l.Spec.EndpointA.NodeName]
		l.Spec.EndpointB.NodeID = idByName[l.Spec.EndpointB.NodeName]
	}
}

// planChanges diffs the deployed base topology against the rebuilt
// desired one into the plan's operation list: nodes and links only in
// the result are created, only in the base are deleted, and surviving
// nodes whose spec or link set changed get an update (their rendered
// config changes). Warnings surface consequences the operations alone
// do not show, e.g. programs living on servers about to be removed.
func planChanges(base *topology.Base, res *topology.Result,
	programs []*model.Program) ([]model.PlanOperation, []model.PlanWarning) {
	var baseNodes []*model.Node

	var baseLinks []*model.Link
	if base != nil {
		baseNodes, baseLinks = base.Nodes, base.Links
	}

	nodeInBase := make(map[string]*model.Node, len(baseNodes))
	for _, n := range baseNodes {
		nodeInBase[n.Meta.Name] = n
	}

	linkInBase := make(map[string]*model.Link, len(baseLinks))
	for _, l := range baseLinks {
		linkInBase[l.Meta.Name] = l
	}

	nodeInRes := make(map[string]*model.Node, len(res.Nodes))
	for _, n := range res.Nodes {
		nodeInRes[n.Meta.Name] = n
	}

	linkInRes := make(map[string]bool, len(res.Links))
	for _, l := range res.Links {
		linkInRes[l.Meta.Name] = true
	}

	var ops []model.PlanOperation

	// touched counts link changes per surviving node: its rendered
	// config (BGP neighbors, bridge ports, bond members) follows its
	// link set even when the node spec itself is unchanged.
	touched := make(map[string]int)
	for _, n := range res.Nodes {
		if nodeInBase[n.Meta.Name] != nil {
			continue
		}

		summary := fmt.Sprintf("%s (%s)", n.Meta.Name, n.Spec.Role)
		if n.Spec.IsRouter() {
			summary = fmt.Sprintf("%s (%s, AS%d, lo %s)", n.Meta.Name, n.Spec.Role, n.Spec.ASN, n.Spec.Loopback)
		}

		ops = append(ops, model.PlanOperation{Type: model.PlanCreateNode, Target: n.Meta.Name, Summary: summary})
	}

	for _, l := range res.Links {
		if linkInBase[l.Meta.Name] != nil {
			continue
		}

		ops = append(ops, model.PlanOperation{
			Type:   model.PlanCreateLink,
			Target: l.Meta.Name,
			Summary: fmt.Sprintf("%s:%s (%s) <-> %s:%s (%s)",
				l.Spec.EndpointA.NodeName, l.Spec.EndpointA.Interface, l.Spec.EndpointA.Address,
				l.Spec.EndpointB.NodeName, l.Spec.EndpointB.Interface, l.Spec.EndpointB.Address),
		})
		for _, name := range []string{l.Spec.EndpointA.NodeName, l.Spec.EndpointB.NodeName} {
			if nodeInBase[name] != nil {
				touched[name]++
			}
		}
	}

	for _, l := range baseLinks {
		if linkInRes[l.Meta.Name] {
			continue
		}

		ops = append(ops, model.PlanOperation{
			Type:   model.PlanDeleteLink,
			Target: l.Meta.Name,
			Summary: fmt.Sprintf("%s:%s <-> %s:%s",
				l.Spec.EndpointA.NodeName, l.Spec.EndpointA.Interface,
				l.Spec.EndpointB.NodeName, l.Spec.EndpointB.Interface),
		})
		for _, name := range []string{l.Spec.EndpointA.NodeName, l.Spec.EndpointB.NodeName} {
			if nodeInRes[name] != nil && nodeInBase[name] != nil {
				touched[name]++
			}
		}
	}

	for _, n := range baseNodes {
		if nodeInRes[n.Meta.Name] != nil {
			continue
		}

		ops = append(ops, model.PlanOperation{
			Type: model.PlanDeleteNode, Target: n.Meta.Name,
			Summary: fmt.Sprintf("%s (%s)", n.Meta.Name, n.Spec.Role),
		})
	}

	for _, n := range res.Nodes {
		old, ok := nodeInBase[n.Meta.Name]
		if !ok {
			continue
		}

		specChanged := !reflect.DeepEqual(old.Spec, n.Spec)
		if !specChanged && touched[n.Meta.Name] == 0 {
			continue
		}

		summary := fmt.Sprintf("refresh rendered config (%d link change(s))", touched[n.Meta.Name])
		if specChanged {
			summary = "node spec changed; refresh rendered config"
		}

		ops = append(ops, model.PlanOperation{Type: model.PlanUpdateNode, Target: n.Meta.Name, Summary: summary})
	}

	return ops, removedServerWarnings(baseNodes, nodeInRes, programs)
}

// removedServerWarnings lists the programs deployed on servers a plan
// is about to remove: applying the plan deletes them with the server.
func removedServerWarnings(baseNodes []*model.Node, nodeInRes map[string]*model.Node,
	programs []*model.Program) []model.PlanWarning {
	byServer := make(map[string][]string)
	for _, p := range programs {
		byServer[p.Spec.ServerName] = append(byServer[p.Spec.ServerName], p.Meta.Name)
	}

	var warnings []model.PlanWarning
	for _, n := range baseNodes {
		if n.Spec.Role != model.RoleServer || nodeInRes[n.Meta.Name] != nil {
			continue
		}

		names := byServer[n.Meta.Name]
		if len(names) == 0 {
			continue
		}

		sort.Strings(names)
		warnings = append(warnings, model.PlanWarning{
			Code: "ProgramsOnRemovedServer",
			Message: fmt.Sprintf("server %s will be removed together with its %d program(s): %s",
				n.Meta.Name, len(names), strings.Join(names, ", ")),
		})
	}

	return warnings
}
