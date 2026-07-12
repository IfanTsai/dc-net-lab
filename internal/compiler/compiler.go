// Package compiler assembles all deployable artifacts for one lab
// generation: the Containerlab topology plus per-node FRR configs.
package compiler

import (
	"fmt"
	"path/filepath"

	"github.com/ifantsai/dcnetlab/internal/compiler/containerlab"
	"github.com/ifantsai/dcnetlab/internal/compiler/frr"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// Artifact is the compiled output of one generation.
type Artifact struct {
	LabID        string
	LabName      string
	ClabTopology []byte
	// Files maps a path relative to the generation directory
	// (e.g. "configs/leaf-1/frr.conf") to its content.
	Files map[string][]byte
}

// Compile builds the full artifact set for a lab. Internet access is
// a lab-spec property, so it is derived here rather than left to the
// caller's options.
func Compile(lab *model.Lab, nodes []*model.Node, links []*model.Link, opts containerlab.Options) (*Artifact, error) {
	opts.InternetAccess = lab.Spec.Topology.InternetAccess

	routerCfgs, err := frr.BuildRouterConfigs(nodes, links, opts.InternetAccess)
	if err != nil {
		return nil, fmt.Errorf("build frr configs: %w", err)
	}

	files := make(map[string][]byte)
	for _, n := range nodes {
		cfg, ok := routerCfgs[n.Meta.ID]
		if !ok {
			continue
		}

		rendered, err := frr.Render(cfg)
		if err != nil {
			return nil, fmt.Errorf("render frr config for %s: %w", n.Meta.Name, err)
		}

		dir := filepath.Join("configs", n.Meta.Name)
		files[filepath.Join(dir, "frr.conf")] = rendered
		files[filepath.Join(dir, "daemons")] = []byte(frr.Daemons)
		files[filepath.Join(dir, "vtysh.conf")] = []byte(frr.VtyshConf)
	}

	clabYAML, err := containerlab.Compile(lab.Meta.Name, nodes, links, opts)
	if err != nil {
		return nil, fmt.Errorf("compile containerlab topology: %w", err)
	}

	return &Artifact{
		LabID:        lab.Meta.ID,
		LabName:      lab.Meta.Name,
		ClabTopology: clabYAML,
		Files:        files,
	}, nil
}
