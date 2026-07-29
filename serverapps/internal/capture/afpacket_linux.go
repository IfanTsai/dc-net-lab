//go:build linux

package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
	"golang.org/x/sys/unix"
)

// recvTimeout bounds a blocking read so the loop notices context
// cancellation (stdin EOF, signals) promptly even on a silent link.
const recvTimeout = 250 * time.Millisecond

// Run captures on the configured interface until the context is
// cancelled, the duration elapses or a packet/byte limit is reached,
// writing a pcapng stream to out. Each record carries the kernel's
// original packet length next to the snapped capture length, plus the
// direction flag Wireshark displays.
func Run(ctx context.Context, out io.Writer, opts Options) error {
	if err := opts.normalise(); err != nil {
		return err
	}

	ifc, err := net.InterfaceByName(opts.Iface)
	if err != nil {
		return fmt.Errorf("resolve interface %s: %w", opts.Iface, err)
	}

	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return fmt.Errorf("open AF_PACKET socket: %w", err)
	}
	defer unix.Close(fd) //nolint:errcheck // read-only teardown

	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_ALL),
		Ifindex:  ifc.Index,
	}); err != nil {
		return fmt.Errorf("bind to %s: %w", opts.Iface, err)
	}

	tv := unix.NsecToTimeval(recvTimeout.Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return fmt.Errorf("set receive timeout: %w", err)
	}

	w, err := pcapgo.NewNgWriterInterface(out, pcapgo.NgInterface{
		Name:       opts.Iface,
		OS:         "linux",
		LinkType:   layers.LinkTypeEthernet,
		SnapLength: uint32(opts.SnapLen),
	}, pcapgo.NgWriterOptions{
		SectionInfo: pcapgo.NgSectionInfo{Application: "dcnetlab-capture"},
	})
	if err != nil {
		return fmt.Errorf("write pcapng header: %w", err)
	}
	defer w.Flush() //nolint:errcheck // best effort: stdout is gone when this fails

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush pcapng header: %w", err)
	}

	deadline := time.Now().Add(opts.Duration)
	buf := make([]byte, opts.SnapLen)

	var packets, bytes uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if time.Now().After(deadline) {
			return nil
		}

		// MSG_TRUNC makes n the original wire length even when the
		// frame is snapped to the buffer size.
		n, from, err := unix.Recvfrom(fd, buf, unix.MSG_TRUNC)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
				continue
			}

			// The interface going admin-down mid-capture surfaces as
			// ENETDOWN — and capturing across exactly that (a link-down
			// fault and its recovery) is a core use case. The bound
			// socket stays valid, so wait out the outage; recvfrom
			// returns immediately while the interface is down, hence
			// the pause to avoid a busy loop.
			if errors.Is(err, unix.ENETDOWN) {
				time.Sleep(recvTimeout)

				continue
			}

			return fmt.Errorf("receive on %s: %w", opts.Iface, err)
		}

		sll, ok := from.(*unix.SockaddrLinklayer)
		if !ok || n == 0 {
			continue
		}

		if !directionAllowed(opts.Direction, sll.Pkttype) {
			continue
		}

		capLen := min(n, len(buf))
		if !opts.Filter.Match(buf[:capLen]) {
			continue
		}

		if err := writePacket(w, buf[:capLen], n, sll.Pkttype); err != nil {
			return fmt.Errorf("write packet: %w", err)
		}

		// Flush per packet: the reader renders live and lab traffic
		// rates are nowhere near where batching would matter.
		if err := w.Flush(); err != nil {
			return fmt.Errorf("flush packet: %w", err)
		}

		packets++
		bytes += uint64(capLen)

		if opts.MaxPackets != 0 && packets >= opts.MaxPackets {
			return nil
		}

		if opts.MaxBytes != 0 && bytes >= opts.MaxBytes {
			return nil
		}
	}
}

func writePacket(w *pcapgo.NgWriter, data []byte, wireLen int, pkttype uint8) error {
	flags := pcapgo.NgEpbFlags{Direction: pcapgo.NgEpbFlagDirectionInbound}
	if pkttype == linuxPacketOutgoing {
		flags.Direction = pcapgo.NgEpbFlagDirectionOutbound
	}

	return w.WritePacketWithOptions(gopacket.CaptureInfo{
		Timestamp:     time.Now(),
		CaptureLength: len(data),
		Length:        wireLen,
	}, data, pcapgo.NgPacketOptions{Flags: &flags})
}

// htons converts to network byte order for the socket protocol field.
func htons(v uint16) uint16 {
	return v<<8 | v>>8
}
