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
