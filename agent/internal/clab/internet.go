package clab

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// WANNetworkName is the docker bridge network shared by every lab's
// external routers. It stands for "beyond the carrier": docker NATs
// it to the real internet, so a packet leaving an external node on
// this network has genuinely left the simulation.
const WANNetworkName = "dcnetlab-wan"

// EnsureImage verifies the image is present locally so a deploy that
// needs it fails early with a clear message instead of a mid-deploy
// pull error against a registry that does not have it.
func (d *ContainerlabDriver) EnsureImage(ctx context.Context, image string) error {
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
		return fmt.Errorf("image %s not found locally", image)
	}

	return nil
}

// ConnectInternet attaches a deployed node to the shared WAN network,
// points its default route at the WAN gateway and masquerades
// everything leaving through it behind the node's WAN address (the
// docker host only NATs sources from the WAN subnet itself). The
// sequence is idempotent, so re-applying a generation is safe.
func (d *ContainerlabDriver) ConnectInternet(ctx context.Context, labName, nodeName string) error {
	container := fmt.Sprintf("clab-%s-%s", labName, nodeName)

	if err := ensureWANNetwork(ctx); err != nil {
		return err
	}

	if err := connectToWAN(ctx, container); err != nil {
		return err
	}

	ip, gateway, err := wanEndpoint(ctx, container)
	if err != nil {
		return err
	}

	iface, err := d.wanInterface(ctx, labName, nodeName, ip)
	if err != nil {
		return err
	}

	script := fmt.Sprintf(
		"ip route replace default via %s dev %s && "+
			"{ iptables -t nat -C POSTROUTING -o %s -j MASQUERADE 2>/dev/null || "+
			"iptables -t nat -A POSTROUTING -o %s -j MASQUERADE; }",
		gateway, iface, iface, iface)
	if out, err := d.Exec(ctx, labName, nodeName, []string{"sh", "-c", script}); err != nil {
		return fmt.Errorf("configure WAN uplink on %s: %w\n%s", nodeName, err, out)
	}

	return nil
}

// ensureWANNetwork creates the shared WAN network on first use. The
// network intentionally survives lab destroys: it carries no per-lab
// state and other labs may be using it.
func ensureWANNetwork(ctx context.Context) error {
	if exec.CommandContext(ctx, "docker", "network", "inspect", WANNetworkName).Run() == nil {
		return nil
	}

	out, err := exec.CommandContext(ctx, "docker", "network", "create", WANNetworkName).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("create WAN network %s: %w\n%s", WANNetworkName, err, out)
	}

	return nil
}

// connectToWAN attaches the container to the WAN network, tolerating
// a previous attachment. Left to itself docker names the interface
// eth<n> from its own endpoint counter, which knows nothing about the
// ethN veths containerlab already moved into the namespace; since
// Docker 28 the com.docker.network.endpoint.ifname option pins a
// collision-free name instead (and 29 no longer advances the counter
// on failure, so colliding would retry the same name forever). Older
// daemons ignore the option and fail the connect with "file exists" —
// but advance their counter doing so, so retrying walks past the
// fabric interfaces to the first free name.
func connectToWAN(ctx context.Context, container string) error {
	const maxAttempts = 32

	var lastOut []byte
	for range maxAttempts {
		out, err := exec.CommandContext(ctx, "docker", "network", "connect",
			"--driver-opt", "com.docker.network.endpoint.ifname=wan0",
			WANNetworkName, container).CombinedOutput()
		if err == nil || strings.Contains(string(out), "already exists in network") {
			return nil
		}

		if !strings.Contains(string(out), "file exists") {
			return fmt.Errorf("connect %s to %s: %w\n%s", container, WANNetworkName, err, out)
		}

		lastOut = out
	}

	return fmt.Errorf("connect %s to %s: no free interface name after %d attempts\n%s",
		container, WANNetworkName, maxAttempts, lastOut)
}

// wanEndpoint reads the address and gateway docker assigned to the
// container on the WAN network.
func wanEndpoint(ctx context.Context, container string) (ip, gateway string, err error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		fmt.Sprintf(`{{with index .NetworkSettings.Networks %q}}{{.IPAddress}} {{.Gateway}}{{end}}`, WANNetworkName),
		container).Output()
	if err != nil {
		return "", "", fmt.Errorf("inspect WAN endpoint of %s: %w", container, err)
	}

	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return "", "", fmt.Errorf("container %s has no WAN address (got %q)", container, strings.TrimSpace(string(out)))
	}

	return fields[0], fields[1], nil
}

// wanInterface resolves which interface inside the container carries
// the WAN address. The name cannot be assumed: docker numbers its
// endpoints independently of the ethN interfaces containerlab already
// moved into the namespace.
func (d *ContainerlabDriver) wanInterface(ctx context.Context, labName, nodeName, ip string) (string, error) {
	out, err := d.Exec(ctx, labName, nodeName, []string{"ip", "-o", "-4", "addr", "show"})
	if err != nil {
		return "", fmt.Errorf("list addresses on %s: %w", nodeName, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && strings.HasPrefix(fields[3], ip+"/") {
			return strings.SplitN(fields[1], "@", 2)[0], nil
		}
	}

	return "", fmt.Errorf("no interface on %s carries WAN address %s", nodeName, ip)
}
