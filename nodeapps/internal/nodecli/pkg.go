// Package nodecli implements the in-container operator CLI shipped
// as the node-cli multi-call binary. The pkg command is the package
// manager: it lists the controller's repository and installs/removes
// packages through the local node-agent daemon, mirroring how an
// operator would use a distro package manager on a real server.
package nodecli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ifantsai/dcnetlab/internal/nodeagentapi"
	"github.com/ifantsai/dcnetlab/nodeapps/internal/nodeagent"
	pb "github.com/ifantsai/dcnetlab/pb/nodeagent/v1"
)

const (
	pkgUsage = `usage: pkg [flags] <command>        (equivalent: node-cli pkg ...)

commands:
  list                     list repository packages and their local state
  list <name>              list the versions of one package
  install <name>[@<ver>]   install a package (latest version by default)
  remove <name>[@<ver>]    remove local versions unused by programs

flags (before the command):
  --repo <url>    repository base URL (default: $DCNETLAB_REPO, the
                  URL remembered by the agent, or the management
                  gateway on port %d)
  --dir <path>    agent state directory (default /opt/dcnetlab/run)
  --agent <addr>  local agent gRPC address (default 127.0.0.1:%d)
`

	pkgInstallTimeout = 2 * time.Minute
	pkgCallTimeout    = 10 * time.Second
)

// PkgMain runs the pkg subcommand and returns the process exit code.
func PkgMain(args []string) int {
	fs := flag.NewFlagSet("pkg", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, pkgUsage, nodeagentapi.DefaultRepoPort, nodeagentapi.DefaultPort)
	}

	repo := fs.String("repo", "", "repository base URL")
	dir := fs.String("dir", "/opt/dcnetlab/run", "agent state directory")
	agentAddr := fs.String("agent", fmt.Sprintf("127.0.0.1:%d", nodeagentapi.DefaultPort), "local agent gRPC address")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cli := &cliCtx{repo: *repo, dir: *dir, agentAddr: *agentAddr, out: os.Stdout}

	var err error
	switch cmd, target := fs.Arg(0), fs.Arg(1); cmd {
	case "list":
		err = cli.list(target)
	case "install":
		err = cli.install(target)
	case "remove":
		err = cli.remove(target)
	default:
		fs.Usage()

		return 2
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "pkg:", err)

		return 1
	}

	return 0
}

// cliCtx carries the resolved configuration of one invocation.
type cliCtx struct {
	repo      string
	dir       string
	agentAddr string
	out       io.Writer
}

// list prints the repository index (optionally one package) with the
// local installation state.
func (c *cliCtx) list(name string) error {
	entries, err := c.fetchIndex(name)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(c.out, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tVERSION\tSTATE\tSIZE\tSHA256\tDESCRIPTION")
	for _, e := range entries {
		state := "-"
		if c.installed(e.Name, e.Version) {
			state = "installed"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.12s\t%s\n",
			e.Name, e.Version, state, formatSize(e.SizeBytes), e.SHA256, e.Description)
	}

	return tw.Flush()
}

// install resolves a name[@version] against the repository and asks
// the local agent daemon to install it.
func (c *cliCtx) install(target string) error {
	name, version, err := splitTarget(target)
	if err != nil {
		return err
	}

	entry, err := c.resolveEntry(name, version)
	if err != nil {
		return err
	}

	if err := c.installEntry(entry); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(c.out, "installed %s@%s\n", entry.Name, entry.Version)

	return nil
}

// resolveEntry finds one package version in the repository index;
// an empty version picks the latest (the index is newest first).
func (c *cliCtx) resolveEntry(name, version string) (*repoEntry, error) {
	entries, err := c.fetchIndex(name)
	if err != nil {
		return nil, err
	}

	for i := range entries {
		if version == "" || entries[i].Version == version {
			return &entries[i], nil
		}
	}

	return nil, fmt.Errorf("package %s@%s not found in the repository", name, version)
}

// installEntry asks the local agent daemon to install one resolved
// package version (a no-op when already present).
func (c *cliCtx) installEntry(entry *repoEntry) error {
	base, err := c.repoBase()
	if err != nil {
		return err
	}

	ref := &pb.PackageRef{
		Name:       entry.Name,
		Version:    entry.Version,
		Sha256:     entry.SHA256,
		Url:        fmt.Sprintf("%s/packages/%s/%s", base, entry.Name, entry.Version),
		Entrypoint: entry.Entrypoint,
	}

	return c.callAgent(pkgInstallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		_, err := client.InstallPackage(ctx, &pb.InstallPackageRequest{Package: ref})

		return err
	})
}

// remove asks the local agent daemon to delete local package
// versions. Versions referenced by programs are kept; when that
// leaves nothing removed, the command fails so scripts notice.
func (c *cliCtx) remove(target string) error {
	name, version, err := splitTarget(target)
	if err != nil {
		return err
	}

	var reply *pb.RemovePackageReply
	err = c.callAgent(pkgCallTimeout, func(ctx context.Context, client pb.NodeAgentClient) error {
		var err error
		reply, err = client.RemovePackage(ctx, &pb.RemovePackageRequest{Name: name, Version: version})

		return err
	})
	if err != nil {
		return err
	}

	if len(reply.Removed) == 0 && len(reply.InUse) > 0 {
		return fmt.Errorf("%s; remove the referencing program first (pkg cannot remove versions programs run out of)",
			inUseClauses(name, reply.InUse))
	}

	for _, v := range reply.Removed {
		_, _ = fmt.Fprintf(c.out, "removed %s@%s\n", name, v)
	}

	for _, u := range reply.InUse {
		_, _ = fmt.Fprintf(c.out, "kept: %s\n", inUseClause(name, u))
	}

	if len(reply.Removed) == 0 && len(reply.InUse) == 0 {
		_, _ = fmt.Fprintln(c.out, "nothing to remove")
	}

	return nil
}

// inUseClause renders one kept version with the programs pinning it,
// e.g. "demo@2.0.0 is in use by program demo1".
func inUseClause(name string, u *pb.InUseVersion) string {
	noun := "program"
	if len(u.Programs) > 1 {
		noun = "programs"
	}

	return fmt.Sprintf("%s@%s is in use by %s %s", name, u.Version, noun, strings.Join(u.Programs, ", "))
}

func inUseClauses(name string, inUse []*pb.InUseVersion) string {
	clauses := make([]string, 0, len(inUse))
	for _, u := range inUse {
		clauses = append(clauses, inUseClause(name, u))
	}

	return strings.Join(clauses, "; ")
}

// repoEntry mirrors the repository index JSON.
type repoEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Entrypoint  string `json:"entrypoint"`
	Description string `json:"description"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
	Builtin     bool   `json:"builtin"`
}

// fetchIndex downloads the repository index, optionally filtered to
// one package name.
func (c *cliCtx) fetchIndex(name string) ([]repoEntry, error) {
	base, err := c.repoBase()
	if err != nil {
		return nil, err
	}

	url := base + "/packages"
	if name != "" {
		url += "/" + name
	}

	ctx, cancel := context.WithTimeout(context.Background(), pkgCallTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach repository %s: %w (override with --repo)", base, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound && name != "" {
		return nil, fmt.Errorf("package %s not found in the repository", name)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repository %s: status %s", url, resp.Status)
	}

	var entries []repoEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse repository index: %w", err)
	}

	return entries, nil
}

// repoBase resolves the repository base URL: the --repo flag, the
// DCNETLAB_REPO environment, the base remembered by the agent daemon,
// or the management gateway guessed from eth0.
func (c *cliCtx) repoBase() (string, error) {
	if c.repo != "" {
		return strings.TrimSuffix(c.repo, "/"), nil
	}

	if env := os.Getenv("DCNETLAB_REPO"); env != "" {
		return strings.TrimSuffix(env, "/"), nil
	}

	if base, ok := nodeagent.RememberedRepo(c.dir); ok {
		return base, nil
	}

	gw, err := managementGateway()
	if err != nil {
		return "", fmt.Errorf("cannot locate the repository: %w (use --repo)", err)
	}

	return fmt.Sprintf("http://%s", net.JoinHostPort(gw, fmt.Sprint(nodeagentapi.DefaultRepoPort))), nil
}

// managementGateway guesses the host-side gateway of the management
// network: the first address of eth0's IPv4 subnet (the Docker
// bridge convention).
func managementGateway() (string, error) {
	iface, err := net.InterfaceByName("eth0")
	if err != nil {
		return "", fmt.Errorf("no management interface: %w", err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}

	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.To4() == nil {
			continue
		}

		prefix, err := netip.ParsePrefix(ipNet.String())
		if err != nil {
			continue
		}

		return prefix.Masked().Addr().Next().String(), nil
	}

	return "", fmt.Errorf("eth0 has no IPv4 address")
}

// installed reports whether a package version is complete in the
// local store.
func (c *cliCtx) installed(name, version string) bool {
	return nodeagent.PackageInstalled(c.dir, name, version)
}

// callAgent runs one RPC against the local agent daemon.
func (c *cliCtx) callAgent(timeout time.Duration, fn func(ctx context.Context, client pb.NodeAgentClient) error) error {
	conn, err := grpc.NewClient(c.agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial agent %s: %w", c.agentAddr, err)
	}

	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := fn(ctx, pb.NewNodeAgentClient(conn)); err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	return nil
}

// splitTarget parses name[@version].
func splitTarget(target string) (name, version string, err error) {
	if target == "" {
		return "", "", fmt.Errorf("package name required")
	}

	name, version, _ = strings.Cut(target, "@")
	if name == "" {
		return "", "", fmt.Errorf("invalid package reference %q", target)
	}

	return name, version, nil
}

// formatSize renders a byte count for humans.
func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
