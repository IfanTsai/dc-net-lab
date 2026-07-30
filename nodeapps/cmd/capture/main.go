// dcnetlab-capture is the in-container packet capture tool, mounted
// into every lab node (conceptually it ships with the NOS image). The
// controller runs it via docker exec with stdout carrying a pcapng
// stream; closing stdin stops the capture, so the session can never
// outlive its exec connection. Operators can also run it by hand from
// a node terminal: capture --iface eth1 > /tmp/x.pcapng
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ifantsai/dcnetlab/nodeapps/internal/capture"
)

func main() {
	iface := flag.String("iface", "", "interface to capture on (required)")
	snap := flag.Int("snap", 256, "bytes to keep of each packet")
	duration := flag.Duration("duration", 30*time.Second, "hard stop after this long")
	direction := flag.String("direction", capture.DirectionBoth, "both, rx or tx")
	maxPackets := flag.Uint64("max-packets", 0, "stop after this many packets (0 = unlimited)")
	maxBytes := flag.Uint64("max-bytes", 0, "stop after this many captured bytes (0 = unlimited)")
	proto := flag.String("proto", "", "protocol filter: arp, icmp, tcp, udp, bgp or vxlan")
	src := flag.String("src", "", "source address or prefix filter")
	dst := flag.String("dst", "", "destination address or prefix filter")
	port := flag.Uint("port", 0, "transport port filter (either side)")
	flag.Parse()

	if err := run(capture.Options{
		Iface:      *iface,
		SnapLen:    *snap,
		Duration:   *duration,
		Direction:  *direction,
		MaxPackets: *maxPackets,
		MaxBytes:   *maxBytes,
	}, *proto, *src, *dst, *port); err != nil {
		fmt.Fprintf(os.Stderr, "capture: %v\n", err)
		os.Exit(1)
	}
}

func run(opts capture.Options, proto, src, dst string, port uint) error {
	filter, err := buildFilter(proto, src, dst, port)
	if err != nil {
		return err
	}

	opts.Filter = filter

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The controller owns the session through stdin: EOF (or a dropped
	// exec connection) cancels the capture.
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()

		_, _ = io.Copy(io.Discard, os.Stdin)
	}()

	return capture.Run(ctx, os.Stdout, opts)
}

func buildFilter(proto, src, dst string, port uint) (capture.Filter, error) {
	if port > 65535 {
		return capture.Filter{}, fmt.Errorf("port %d out of range", port)
	}

	srcPrefix, err := parsePrefix(src)
	if err != nil {
		return capture.Filter{}, fmt.Errorf("parse src filter: %w", err)
	}

	dstPrefix, err := parsePrefix(dst)
	if err != nil {
		return capture.Filter{}, fmt.Errorf("parse dst filter: %w", err)
	}

	return capture.Filter{
		Protocol: proto,
		Src:      srcPrefix,
		Dst:      dstPrefix,
		Port:     uint16(port),
	}, nil
}

// parsePrefix accepts a CIDR prefix or a bare address (treated as a
// host prefix); empty means no restriction.
func parsePrefix(s string) (netip.Prefix, error) {
	if s == "" {
		return netip.Prefix{}, nil
	}

	if p, err := netip.ParsePrefix(s); err == nil {
		return p, nil
	}

	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is neither a prefix nor an address", s)
	}

	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
