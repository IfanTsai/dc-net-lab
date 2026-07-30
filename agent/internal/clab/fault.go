package clab

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// netemArgs builds the "netem ..." tail of a tc qdisc command from
// the set fields of imp, in a fixed order matching tc's own netem
// argument grammar (delay [jitter], loss, rate).
func netemArgs(imp runtime.Impairment) []string {
	var args []string

	if imp.DelayMs > 0 {
		args = append(args, "delay", strconv.Itoa(imp.DelayMs)+"ms")
		if imp.JitterMs > 0 {
			args = append(args, strconv.Itoa(imp.JitterMs)+"ms")
		}
	}

	if imp.LossPercent > 0 {
		args = append(args, "loss", strconv.FormatFloat(imp.LossPercent, 'g', -1, 64)+"%")
	}

	if imp.RateKbit > 0 {
		args = append(args, "rate", strconv.Itoa(imp.RateKbit)+"kbit")
	}

	return args
}

// SetInterfaceState brings a node's interface administratively up or
// down (ip link set), used for link-down and interface-down faults.
// ip link set is idempotent: setting an already-up interface up (or
// an already-down interface down) succeeds without effect, so recover
// needs no prior-state check.
func (d *ContainerlabDriver) SetInterfaceState(ctx context.Context, labName, nodeName, iface string, up bool) error {
	state := "down"
	if up {
		state = "up"
	}

	if out, err := d.Exec(ctx, labName, nodeName, []string{"ip", "link", "set", iface, state}); err != nil {
		return fmt.Errorf("set %s on %s:%s %s: %w\n%s", iface, nodeName, iface, state, err, out)
	}

	return nil
}

// ApplyImpairment shapes egress traffic on a node's interface with a
// netem qdisc built from imp. "qdisc replace" creates or overwrites
// the interface's root qdisc in one idempotent call, so re-applying
// (or applying a changed spec while the old one is still active)
// needs no read-modify-write of the existing qdisc.
func (d *ContainerlabDriver) ApplyImpairment(ctx context.Context, labName, nodeName, iface string, imp runtime.Impairment) error {
	netem := netemArgs(imp)
	if len(netem) == 0 {
		return fmt.Errorf("impairment on %s:%s has no delay/loss/rate set", nodeName, iface)
	}

	args := append([]string{"tc", "qdisc", "replace", "dev", iface, "root", "netem"}, netem...)
	if out, err := d.Exec(ctx, labName, nodeName, args); err != nil {
		return fmt.Errorf("apply impairment on %s:%s: %w\n%s", nodeName, iface, err, out)
	}

	return nil
}

// ClearImpairment removes a previously applied netem qdisc from a
// node's interface. It is a no-op (not an error) when no such qdisc
// is present, so recover stays idempotent even if the fault was
// already cleared some other way.
func (d *ContainerlabDriver) ClearImpairment(ctx context.Context, labName, nodeName, iface string) error {
	out, err := d.Exec(ctx, labName, nodeName, []string{"tc", "qdisc", "show", "dev", iface, "root"})
	if err != nil {
		return fmt.Errorf("inspect qdisc on %s:%s: %w\n%s", nodeName, iface, err, out)
	}

	if !strings.Contains(string(out), "netem") {
		return nil
	}

	if out, err := d.Exec(ctx, labName, nodeName, []string{"tc", "qdisc", "del", "dev", iface, "root"}); err != nil {
		return fmt.Errorf("clear impairment on %s:%s: %w\n%s", nodeName, iface, err, out)
	}

	return nil
}
