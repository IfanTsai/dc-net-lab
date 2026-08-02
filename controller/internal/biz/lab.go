package biz

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ifantsai/dcnetlab/controller/internal/conf"
	"github.com/ifantsai/dcnetlab/controller/internal/operation"
	"github.com/ifantsai/dcnetlab/controller/internal/topology"
	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// LabRepo abstracts the lab persistence the lab usecase needs. The
// data layer provides the implementation; biz never opens connections.
type LabRepo interface {
	CreateLab(lab *model.Lab) error
	UpdateLab(lab *model.Lab) error
	GetLab(id string) (*model.Lab, error)
	ListLabs() ([]*model.Lab, error)
	DeleteLab(id string) error
}

// LabUsecase owns the lab lifecycle: create, query, destroy.
type LabUsecase struct {
	repo    LabRepo
	ops     *operation.Manager
	driver  runtime.Driver
	dataDir string
	log     *slog.Logger
}

// NewLabUsecase wires the lab usecase. The artifact directory shares
// the data directory with the store.
func NewLabUsecase(repo LabRepo, ops *operation.Manager, driver runtime.Driver, c *conf.Data, log *slog.Logger) *LabUsecase {
	return &LabUsecase{repo: repo, ops: ops, driver: driver, dataDir: c.Dir, log: log}
}

// CreateLab registers a new lab with the given profile. For the
// custom profile a topology spec must be provided. internetAccess
// applies to every profile: built-in profiles carry no such knob of
// their own, so it rides on the request rather than the spec.
func (uc *LabUsecase) CreateLab(name string, profile model.ProfileName, custom *model.TopologySpec, internetAccess bool) (*model.Lab, error) {
	if name == "" {
		return nil, fmt.Errorf("lab name is required")
	}

	var topo model.TopologySpec
	if profile == model.ProfileCustom {
		if custom == nil {
			return nil, fmt.Errorf("custom profile requires a topology spec")
		}

		topo = *custom
	} else {
		t, ok := topology.Profiles()[profile]
		if !ok {
			return nil, fmt.Errorf("unknown profile %q", profile)
		}

		topo = t
	}

	topo.InternetAccess = topo.InternetAccess || internetAccess

	now := time.Now().UTC()
	lab := &model.Lab{
		Meta: model.ResourceMeta{
			ID: model.NewID("lab"), Name: name,
			Phase: model.PhasePending, CreatedAt: now, UpdatedAt: now,
		},
		Spec: model.LabSpec{
			Profile:  profile,
			Topology: topo,
			Pools:    topology.DefaultPools(),
			ASNs:     topology.DefaultASNRanges(),
		},
	}

	if err := uc.repo.CreateLab(lab); err != nil {
		return nil, fmt.Errorf("persist lab: %w", err)
	}

	return lab, nil
}

// UpdateTopology changes the desired fabric shape (scale out / in).
// It only edits the spec: the change materialises through the regular
// Plan (diff preview) / Apply (incremental deploy) flow. The internet
// attachment is fixed at lab creation, and existing pods keep their
// names — pod identity is positional, so pods can be appended,
// resized or removed from the tail, but not renamed.
func (uc *LabUsecase) UpdateTopology(labID string, topo model.TopologySpec) (*model.Lab, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	if lab.Meta.Phase == model.PhaseApplying {
		return nil, fmt.Errorf("lab %q is applying a plan; wait for it to finish", lab.Meta.Name)
	}

	if err := validateTopologySpec(topo); err != nil {
		return nil, err
	}

	topo.InternetAccess = lab.Spec.Topology.InternetAccess
	if err := fillPodNames(topo.Pods, lab.Spec.Topology.Pods); err != nil {
		return nil, err
	}

	lab.Spec.Topology = topo
	// A resized lab no longer matches its built-in profile.
	lab.Spec.Profile = model.ProfileCustom
	lab.Meta.UpdatedAt = time.Now().UTC()

	if err := uc.repo.UpdateLab(lab); err != nil {
		return nil, fmt.Errorf("update lab: %w", err)
	}

	return lab, nil
}

// validateTopologySpec bounds a desired fabric shape: every tier
// present, and each rack's servers fitting the .11+ host range of its
// /24 subnet.
func validateTopologySpec(t model.TopologySpec) error {
	if t.ExternalRouters < 1 || t.DCEdges < 1 || t.SuperSpines < 1 {
		return fmt.Errorf("external routers, dc edges and super spines must each be at least 1")
	}

	if len(t.Pods) == 0 {
		return fmt.Errorf("at least one pod is required")
	}

	for i, p := range t.Pods {
		if p.Spines < 1 || p.Racks < 1 || p.ServersPerRack < 1 {
			return fmt.Errorf("pod %d: spines, racks and servers per rack must each be at least 1", i+1)
		}

		if p.ServersPerRack > 200 {
			return fmt.Errorf("pod %d: at most 200 servers per rack (rack subnet is a /24)", i+1)
		}
	}

	return nil
}

// fillPodNames resolves pod identity for a topology update. Pods are
// identified by name, not slot, so any pod may be removed (the rest
// keep their names and thereby their deployed nodes); names must stay
// unique, and unnamed new pods get the next never-used default name.
// A pod whose name matches nothing deployed is simply a new pod — the
// plan preview shows the delete/create pair such an edit produces.
func fillPodNames(pods []model.PodSpec, prev []model.PodSpec) error {
	used := make(map[string]bool)
	for i := range pods {
		if pods[i].Name == "" {
			continue
		}

		if used[pods[i].Name] {
			return fmt.Errorf("pod name %q used twice", pods[i].Name)
		}

		used[pods[i].Name] = true
	}

	// Unnamed slots first reclaim their previous slot's name (the
	// common "edit counts in place" shape), then fall back to fresh
	// default names.
	for i := range pods {
		if pods[i].Name != "" {
			continue
		}

		if i < len(prev) && prev[i].Name != "" && !used[prev[i].Name] {
			pods[i].Name = prev[i].Name
			used[pods[i].Name] = true
		}
	}

	next := 1
	for i := range pods {
		if pods[i].Name != "" {
			continue
		}

		for used[fmt.Sprintf("pod-%d", next)] {
			next++
		}

		pods[i].Name = fmt.Sprintf("pod-%d", next)
		used[pods[i].Name] = true
	}

	return nil
}

// ListLabs returns all labs.
func (uc *LabUsecase) ListLabs() ([]*model.Lab, error) { return uc.repo.ListLabs() }

// GetLab returns one lab by ID.
func (uc *LabUsecase) GetLab(id string) (*model.Lab, error) { return uc.repo.GetLab(id) }

// DeleteLab tears down the runtime topology and removes the lab.
func (uc *LabUsecase) DeleteLab(labID string) (*model.Operation, error) {
	lab, err := uc.repo.GetLab(labID)
	if err != nil {
		return nil, fmt.Errorf("get lab: %w", err)
	}

	op, err := uc.ops.Create(lab.Meta.ID, model.OperationDestroyLab, model.ResourceRef{Type: "lab", ID: labID})
	if err != nil {
		return nil, fmt.Errorf("create destroy operation: %w", err)
	}

	lab.Meta.Phase = model.PhaseDeleting
	if err := uc.repo.UpdateLab(lab); err != nil {
		return nil, fmt.Errorf("update lab phase: %w", err)
	}

	genDir := generationDir(uc.dataDir, lab.Meta.ID, lab.Meta.Generation)
	steps := []operation.Step{
		{Name: "DestroyTopology", Fn: func(ctx context.Context) error {
			if lab.Meta.Generation == 0 {
				return nil // never applied, nothing running
			}

			return uc.driver.Destroy(ctx, genDir)
		}},
		{Name: "DeleteResources", Fn: func(ctx context.Context) error {
			return uc.repo.DeleteLab(labID)
		}},
	}

	uc.ops.Run(op, steps, nil)

	return op, nil
}
