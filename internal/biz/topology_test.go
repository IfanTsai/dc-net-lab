package biz

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"testing"

	"github.com/ifantsai/dcnetlab/internal/model"
)

type fakeTopologyRepo struct {
	nodes []*model.Node
	links []*model.Link
}

func (r *fakeTopologyRepo) GetLab(id string) (*model.Lab, error) {
	return &model.Lab{Meta: model.ResourceMeta{ID: id}}, nil
}

func (r *fakeTopologyRepo) ListNodes(labID string) ([]*model.Node, error) { return r.nodes, nil }

func (r *fakeTopologyRepo) ListLinks(labID string) ([]*model.Link, error) { return r.links, nil }

func (r *fakeTopologyRepo) ListAllocations(labID string) ([]model.Allocation, error) {
	return nil, nil
}

func TestGetNodeBGP(t *testing.T) {
	spine := &model.Node{
		Meta: model.ResourceMeta{ID: "n-spine", Name: "spine-1"},
		Spec: model.NodeSpec{
			Role: model.RoleSpine, RuntimeType: model.RuntimeFRR, ASN: 65100,
			Loopback: netip.MustParsePrefix("10.1.0.1/32"),
		},
	}

	leaf := &model.Node{
		Meta: model.ResourceMeta{ID: "n-leaf", Name: "leaf-a"},
		Spec: model.NodeSpec{
			Role: model.RoleLeaf, RuntimeType: model.RuntimeFRR, ASN: 65201,
			Loopback: netip.MustParsePrefix("10.1.0.2/32"),
		},
	}

	repo := &fakeTopologyRepo{
		nodes: []*model.Node{spine, leaf},
		links: []*model.Link{{Spec: model.LinkSpec{
			EndpointA: model.LinkEndpoint{
				NodeID: "n-spine", Interface: "eth1", Address: netip.MustParsePrefix("10.0.0.0/31"),
			},
			EndpointB: model.LinkEndpoint{
				NodeID: "n-leaf", Interface: "eth1", Address: netip.MustParsePrefix("10.0.0.1/31"),
			},
		}}},
	}

	uc := NewTopologyUsecase(repo, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := uc.GetNodeBGP("lab-1", "n-leaf")
	if err != nil {
		t.Fatalf("GetNodeBGP: %v", err)
	}

	if cfg.ASN != 65201 || len(cfg.Neighbors) != 1 {
		t.Fatalf("config: ASN=%d neighbors=%+v", cfg.ASN, cfg.Neighbors)
	}

	nb := cfg.Neighbors[0]
	if nb.Address != netip.MustParseAddr("10.0.0.0") || nb.RemoteAS != 65100 || nb.Name != "spine-1" {
		t.Errorf("neighbor: %+v", nb)
	}

	if _, err := uc.GetNodeBGP("lab-1", "n-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing node: got %v, want ErrNotFound", err)
	}
}
