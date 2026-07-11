package asn

import (
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
)

func newTestAllocator(t *testing.T) *Allocator {
	t.Helper()
	a, err := New([]model.ASNRange{
		{Role: model.RoleSpine, Start: 65200, End: 65202},
		{Role: model.RoleLeaf, Start: 4200000000, End: 4200999999},
	})
	if err != nil {
		t.Fatal(err)
	}

	return a
}

func TestAllocateSequential(t *testing.T) {
	a := newTestAllocator(t)
	for i, want := range []uint32{65200, 65201, 65202} {
		got, err := a.Allocate(model.RoleSpine, "n")
		if err != nil {
			t.Fatal(err)
		}

		if got != want {
			t.Errorf("alloc %d: got %d, want %d", i, got, want)
		}
	}

	if _, err := a.Allocate(model.RoleSpine, "n"); err == nil {
		t.Error("expected exhaustion")
	}
}

func TestFourByteASN(t *testing.T) {
	a := newTestAllocator(t)
	got, err := a.Allocate(model.RoleLeaf, "leaf1")
	if err != nil {
		t.Fatal(err)
	}

	if got != 4200000000 {
		t.Errorf("got %d", got)
	}
}

func TestReleaseReuse(t *testing.T) {
	a := newTestAllocator(t)
	x, _ := a.Allocate(model.RoleSpine, "a")
	if err := a.Release(model.RoleSpine, x); err != nil {
		t.Fatal(err)
	}

	y, _ := a.Allocate(model.RoleSpine, "b")
	if y != x {
		t.Errorf("expected reuse of %d, got %d", x, y)
	}

	if err := a.Release(model.RoleSpine, 9999); err == nil {
		t.Error("release of unallocated asn should fail")
	}
}

func TestRestore(t *testing.T) {
	a := newTestAllocator(t)
	if err := a.Restore(model.RoleSpine, 65201, "n2"); err != nil {
		t.Fatal(err)
	}

	got, _ := a.Allocate(model.RoleSpine, "n3")
	if got != 65202 {
		t.Errorf("got %d, want 65202", got)
	}

	if err := a.Restore(model.RoleSpine, 65201, "dup"); err == nil {
		t.Error("duplicate restore should fail")
	}

	if err := a.Restore(model.RoleSpine, 70000, "bad"); err == nil {
		t.Error("out-of-range restore should fail")
	}
}

func TestUnknownRole(t *testing.T) {
	a := newTestAllocator(t)
	if _, err := a.Allocate(model.RoleDCEdge, "x"); err == nil {
		t.Error("expected error for role without range")
	}
}
