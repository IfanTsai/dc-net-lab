package clab

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// incrementTopology mirrors the subset of the compiler's topology YAML
// the incremental deploy needs: node definitions to create containers
// from. The file format is the contract; containerlab itself is
// another reader of the same file.
type incrementTopology struct {
	Name     string `yaml:"name"`
	Topology struct {
		Nodes map[string]incrementNodeDef `yaml:"nodes"`
	} `yaml:"topology"`
}

type incrementNodeDef struct {
	Kind  string   `yaml:"kind"`
	Image string   `yaml:"image"`
	Binds []string `yaml:"binds"`
	Exec  []string `yaml:"exec"`
}

// DeployIncrement applies the increment artifact under dir to the
// running lab. Containerlab cannot add nodes to a deployed lab (its
// deploy refuses on an existing lab name, node filters included), so
// new containers are created directly through docker with
// containerlab-compatible labels — verified to make containerlab
// inspect and destroy adopt them as lab members — and links are wired
// with `containerlab tools veth create`. Unchanged containers are
// never touched. Order matters: containers first (interfaces must
// exist before the setup execs reference them), then links, then the
// new nodes' exec lists, then delta execs and config reloads on
// surviving nodes (deconfiguring neighbors towards removed nodes),
// and container removal last — the veth pairs die with the removed
// containers.
func (d *ContainerlabDriver) DeployIncrement(ctx context.Context, dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, runtime.IncrementFileName))
	if err != nil {
		return fmt.Errorf("read increment artifact: %w", err)
	}

	var inc runtime.Increment
	if err := json.Unmarshal(raw, &inc); err != nil {
		return fmt.Errorf("parse increment artifact: %w", err)
	}

	if inc.Empty() {
		return nil
	}

	rawTopo, err := os.ReadFile(filepath.Join(dir, runtime.TopologyFileName))
	if err != nil {
		return fmt.Errorf("read topology file: %w", err)
	}

	var topo incrementTopology
	if err := yaml.Unmarshal(rawTopo, &topo); err != nil {
		return fmt.Errorf("parse topology file: %w", err)
	}

	for _, n := range inc.AddNodes {
		def, ok := topo.Topology.Nodes[n]
		if !ok {
			return fmt.Errorf("node %s not in topology file", n)
		}

		if err := d.createNode(ctx, dir, inc.LabName, n, def); err != nil {
			return err
		}
	}

	for _, l := range inc.AddLinks {
		if err := d.createVeth(ctx, inc.LabName, l); err != nil {
			return err
		}
	}

	for _, n := range inc.AddNodes {
		if err := d.execCommands(ctx, inc.LabName, n, topo.Topology.Nodes[n].Exec); err != nil {
			return err
		}
	}

	for _, n := range sortedKeys(inc.NodeExec) {
		if err := d.execCommands(ctx, inc.LabName, n, inc.NodeExec[n]); err != nil {
			return err
		}
	}

	for _, n := range inc.ReloadNodes {
		if err := d.reloadFRRConfig(ctx, dir, inc.LabName, n); err != nil {
			return err
		}
	}

	for _, n := range inc.RemoveNodes {
		if err := d.removeNode(ctx, inc.LabName, n); err != nil {
			return err
		}
	}

	return nil
}

// createNode runs a new lab container the way containerlab would:
// same name, hostname, management network, privileges and labels, so
// containerlab inspect/destroy and every docker-by-name code path
// treat it as a first-class lab member. A leftover container from a
// previously failed increment is removed first: an added node holds
// no state worth keeping yet, and recreating it re-runs the whole
// create/wire/exec sequence deterministically.
func (d *ContainerlabDriver) createNode(ctx context.Context, dir, labName, node string, def incrementNodeDef) error {
	container := fmt.Sprintf("clab-%s-%s", labName, node)
	topoPath := filepath.Join(dir, runtime.TopologyFileName)
	if err := d.removeNode(ctx, labName, node); err != nil {
		return err
	}

	args := []string{
		"run", "-d", "--name", container, "--hostname", node,
		// restart=always matches the containers containerlab creates,
		// so a docker daemon restart brings the whole lab back the
		// same way (veth loss included — the drift banner covers all
		// nodes alike).
		"--privileged", "--restart", "always", "--network", "clab",
		"--label", "containerlab=" + labName,
		"--label", "clab-node-name=" + node,
		"--label", "clab-node-longname=" + container,
		"--label", "clab-node-kind=" + def.Kind,
		"--label", "clab-node-group=",
		"--label", "clab-node-type=",
		"--label", "clab-topo-file=" + topoPath,
		"--label", "clab-node-lab-dir=" + filepath.Join(dir, "clab-"+labName, node),
	}

	for _, b := range def.Binds {
		host, cont, ok := strings.Cut(b, ":")
		if !ok {
			return fmt.Errorf("node %s: unrecognised bind %q", node, b)
		}

		if !filepath.IsAbs(host) {
			host = filepath.Join(dir, host)
		}

		args = append(args, "-v", host+":"+cont)
	}

	args = append(args, def.Image)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		if daemonUnavailable(out) {
			return fmt.Errorf("%w: docker run %s: %s", runtime.ErrUnavailable, container, strings.TrimSpace(string(out)))
		}

		return fmt.Errorf("create node %s: %w\n%s", node, err, out)
	}

	return nil
}

// createVeth wires one link through `containerlab tools veth create`,
// which works between any two running containers — including between
// a new node and a surviving one. Any interface already occupying a
// target name is deleted first: a link in the increment does not
// exist in the deployed model, so whatever sits on its name is stale
// — typically the surviving-side end of a scale-in's veth whose peer
// namespace has not been torn down yet (container network namespace
// destruction after docker rm is asynchronous and can lag by
// minutes), or a half-wired link from a previously failed increment.
// Deleting one end removes the pair, and makes the wiring idempotent.
func (d *ContainerlabDriver) createVeth(ctx context.Context, labName string, l runtime.IncrementLink) error {
	for _, ep := range []runtime.IncrementEndpoint{l.A, l.B} {
		// Best effort: the interface usually does not exist at all.
		_, _ = d.Exec(ctx, labName, ep.Node, []string{"ip", "link", "del", ep.Interface})
	}

	a := fmt.Sprintf("clab-%s-%s:%s", labName, l.A.Node, l.A.Interface)
	b := fmt.Sprintf("clab-%s-%s:%s", labName, l.B.Node, l.B.Interface)

	out, err := exec.CommandContext(ctx, d.bin(), "tools", "veth", "create", "-a", a, "-b", b).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create link %s <-> %s: %w\n%s", a, b, err, out)
	}

	return nil
}

// execCommands replays setup commands inside a node's container, the
// runtime equivalent of the topology file's exec list.
func (d *ContainerlabDriver) execCommands(ctx context.Context, labName, node string, cmds []string) error {
	for _, c := range cmds {
		if _, err := d.Exec(ctx, labName, node, []string{"sh", "-c", c}); err != nil {
			return fmt.Errorf("setup %s: %w", node, err)
		}
	}

	return nil
}

// reloadFRRConfig re-applies a node's rendered FRR config against its
// running daemons. frr-reload computes and applies the delta via
// vtysh, so removed neighbors are deconfigured — something `vtysh -f`
// (purely additive) cannot do. The bind-mounted startup config of the
// container still points at the generation it was created from; the
// running config is what matters, and any later full redeploy or
// repair recreates containers from the current generation anyway.
func (d *ContainerlabDriver) reloadFRRConfig(ctx context.Context, dir, labName, node string) error {
	container := fmt.Sprintf("clab-%s-%s", labName, node)
	conf := filepath.Join(dir, "configs", node, "frr.conf")

	out, err := exec.CommandContext(ctx, "docker", "cp", conf, container+":/tmp/dcnetlab-reload.conf").CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy config into %s: %w\n%s", node, err, out)
	}

	if _, err := d.Exec(ctx, labName, node,
		[]string{"python3", "/usr/lib/frr/frr-reload.py", "--reload", "/tmp/dcnetlab-reload.conf"}); err != nil {
		return fmt.Errorf("reload config on %s: %w", node, err)
	}

	return nil
}

// removeNode destroys one node's container; its veth pairs disappear
// with it (the kernel deletes both ends of a pair). A container that
// is already gone is success, keeping increment retries idempotent.
func (d *ContainerlabDriver) removeNode(ctx context.Context, labName, node string) error {
	container := fmt.Sprintf("clab-%s-%s", labName, node)

	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", container).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such container") {
			return nil
		}

		if daemonUnavailable(out) {
			return fmt.Errorf("%w: docker rm %s: %s", runtime.ErrUnavailable, container, strings.TrimSpace(string(out)))
		}

		return fmt.Errorf("remove node %s: %w\n%s", node, err, out)
	}

	return nil
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
