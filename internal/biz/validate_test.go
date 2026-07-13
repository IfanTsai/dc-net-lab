package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/ifantsai/dcnetlab/internal/model"
	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// unavailableDriver simulates a runtime whose docker daemon rejects
// every exec (e.g. socket permission denied).
type unavailableDriver struct {
	runtime.NoopDriver

	calls int
}

func (d *unavailableDriver) Exec(context.Context, string, string, []string) ([]byte, error) {
	d.calls++

	return nil, fmt.Errorf("%w: docker exec clab-dc1-leaf-1: permission denied", runtime.ErrUnavailable)
}

func TestValidateControlPlaneFailsFastOnUnavailableRuntime(t *testing.T) {
	driver := &unavailableDriver{}
	uc := &PlanUsecase{driver: driver, log: slog.Default()}

	lab := &model.Lab{Meta: model.ResourceMeta{Name: "dc1"}}
	nodes := []*model.Node{{
		Meta: model.ResourceMeta{Name: "leaf-1"},
		Spec: model.NodeSpec{Role: model.RoleLeaf, RuntimeType: model.RuntimeFRR},
	}}

	start := time.Now()
	err := uc.validateControlPlane(context.Background(), lab, nodes, nil)

	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("err = %v, want runtime.ErrUnavailable", err)
	}

	if driver.calls != 1 {
		t.Errorf("driver.Exec called %d times, want 1 (no polling on runtime failure)", driver.calls)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("validation took %s, want immediate failure", elapsed)
	}
}
