package biz

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ifantsai/dcnetlab/controller/internal/topology"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// writeConfigs materialises a fake generation directory: one frr.conf
// per node, defaulting to one shared body with per-node overrides.
func writeConfigs(t *testing.T, dir string, nodes []*model.Node, override map[string]string) {
	t.Helper()

	for _, n := range nodes {
		body := "hostname " + n.Meta.Name + "\n"
		if o, ok := override[n.Meta.Name]; ok {
			body = o
		}

		confDir := filepath.Join(dir, "configs", n.Meta.Name)
		if err := os.MkdirAll(confDir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(confDir, "frr.conf"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildIncrementScaleOutRack(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}
	res := buildStandard(t, base, func(spec *model.TopologySpec) { spec.Pods[0].Racks = 3 })

	oldDir, newDir := t.TempDir(), t.TempDir()
	writeConfigs(t, oldDir, baseRes.Nodes, nil)
	// The new rack's neighbors changed the pod-1 spine configs.
	writeConfigs(t, newDir, res.Nodes, map[string]string{
		"pod-1-spine-1": "hostname pod-1-spine-1\nneighbor added\n",
		"pod-1-spine-2": "hostname pod-1-spine-2\nneighbor added\n",
	})

	inc, err := buildIncrement("dc1", base, res.Nodes, res.Links, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(inc.AddNodes) != 4 || !slices.Contains(inc.AddNodes, "pod-1-rack-5-leaf-a") {
		t.Errorf("add nodes: %v", inc.AddNodes)
	}

	if len(inc.AddLinks) != 9 {
		t.Errorf("add links: got %d, want 9", len(inc.AddLinks))
	}

	if len(inc.NodeExec) != 0 {
		t.Errorf("unexpected delta execs: %v", inc.NodeExec)
	}

	slices.Sort(inc.ReloadNodes)
	if want := []string{"pod-1-spine-1", "pod-1-spine-2"}; !slices.Equal(inc.ReloadNodes, want) {
		t.Errorf("reload nodes: got %v, want %v", inc.ReloadNodes, want)
	}

	if len(inc.RemoveNodes) != 0 {
		t.Errorf("unexpected removals: %v", inc.RemoveNodes)
	}
}

func TestBuildIncrementAddServerAttachesLeafPort(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}
	res := buildStandard(t, base, func(spec *model.TopologySpec) { spec.Pods[0].ServersPerRack = 3 })

	oldDir, newDir := t.TempDir(), t.TempDir()
	writeConfigs(t, oldDir, baseRes.Nodes, nil)
	writeConfigs(t, newDir, res.Nodes, nil)

	inc, err := buildIncrement("dc1", base, res.Nodes, res.Links, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}

	// One new server per pod-1 rack, dual-homed: 2 nodes, 4 links.
	if len(inc.AddNodes) != 2 || len(inc.AddLinks) != 4 {
		t.Errorf("got %d nodes / %d links, want 2/4", len(inc.AddNodes), len(inc.AddLinks))
	}

	// All four pod-1 leaves gain one access port each.
	if len(inc.NodeExec) != 4 {
		t.Fatalf("delta execs on %d nodes, want 4: %v", len(inc.NodeExec), inc.NodeExec)
	}

	cmds := inc.NodeExec["pod-1-rack-1-leaf-a"]
	if len(cmds) != 2 || !strings.Contains(cmds[0], "master br0") || !strings.Contains(cmds[1], "vid 1000 pvid untagged") {
		t.Errorf("leaf attach commands: %v", cmds)
	}

	if len(inc.ReloadNodes) != 0 {
		t.Errorf("adding a server must not reload configs: %v", inc.ReloadNodes)
	}
}

func TestBuildIncrementScaleIn(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}
	res := buildStandard(t, base, func(spec *model.TopologySpec) { spec.Pods[1].Racks = 1 })

	oldDir, newDir := t.TempDir(), t.TempDir()
	writeConfigs(t, oldDir, baseRes.Nodes, nil)
	writeConfigs(t, newDir, res.Nodes, map[string]string{
		"pod-2-spine-1": "hostname pod-2-spine-1\nneighbor removed\n",
		"pod-2-spine-2": "hostname pod-2-spine-2\nneighbor removed\n",
	})

	inc, err := buildIncrement("dc1", base, res.Nodes, res.Links, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(inc.AddNodes)+len(inc.AddLinks)+len(inc.NodeExec) != 0 {
		t.Errorf("scale-in produced additions: %+v", inc)
	}

	if len(inc.RemoveNodes) != 4 || !slices.Contains(inc.RemoveNodes, "pod-2-rack-4-leaf-b") {
		t.Errorf("remove nodes: %v", inc.RemoveNodes)
	}

	slices.Sort(inc.ReloadNodes)
	if want := []string{"pod-2-spine-1", "pod-2-spine-2"}; !slices.Equal(inc.ReloadNodes, want) {
		t.Errorf("reload nodes: got %v, want %v", inc.ReloadNodes, want)
	}
}

func TestBuildIncrementMissingOldConfigFallsBackToReload(t *testing.T) {
	baseRes := buildStandard(t, nil, nil)
	base := &topology.Base{Nodes: baseRes.Nodes, Links: baseRes.Links}
	res := buildStandard(t, base, nil)

	oldDir, newDir := t.TempDir(), t.TempDir()
	writeConfigs(t, oldDir, baseRes.Nodes, nil)
	writeConfigs(t, newDir, res.Nodes, nil)
	if err := os.Remove(filepath.Join(oldDir, "configs", "superspine-1", "frr.conf")); err != nil {
		t.Fatal(err)
	}

	inc, err := buildIncrement("dc1", base, res.Nodes, res.Links, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"superspine-1"}; !slices.Equal(inc.ReloadNodes, want) {
		t.Errorf("reload nodes: got %v, want %v", inc.ReloadNodes, want)
	}

	if inc.Empty() {
		t.Error("increment with a reload must not be empty")
	}
}
