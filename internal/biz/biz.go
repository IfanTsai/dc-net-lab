// Package biz implements the business layer: use-case orchestration,
// transaction boundaries and operation creation, one usecase per
// functional module (lab, topology, plan, operation). Each usecase
// depends on a repo interface defined here and implemented by the
// data layer; biz never parses HTTP or protobuf.
package biz

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/wire"

	"github.com/ifantsai/dcnetlab/internal/operation"
)

// operationTTL is how long finished operations stay queryable.
const operationTTL = 10 * time.Minute

// ErrNotFound is returned when a requested resource does not exist;
// the data layer maps missing rows onto it.
var ErrNotFound = errors.New("not found")

// ProviderSet is the business-layer providers.
var ProviderSet = wire.NewSet(
	NewOperationManager,
	NewLabUsecase,
	NewTopologyUsecase,
	NewPlanUsecase,
	NewOperationUsecase,
	NewTerminalUsecase,
	NewPowerUsecase,
	NewRuntimeUsecase,
	NewProgramUsecase,
	NewPackageUsecase,
)

// NewOperationManager wires the async operation executor.
func NewOperationManager(st operation.Store, log *slog.Logger) *operation.Manager {
	return operation.NewManager(st, log, operationTTL)
}

// generationDir returns the artifact directory for one generation of
// a lab, shared by the plan (write) and lab (destroy) usecases.
func generationDir(dataDir, labID string, generation int64) string {
	return filepath.Join(dataDir, "labs", labID, "generations", strconv.FormatInt(generation, 10))
}
