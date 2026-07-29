package capture

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/gopacket/gopacket/pcapgo"
)

// ErrRecordingGone marks a session whose pcapng recording is no
// longer on disk (expired or never written); its metadata row still
// exists.
var ErrRecordingGone = errors.New("capture recording no longer available")

// ReadPage reads packet rows [offset, offset+limit) from a recording
// file, for sessions without live in-memory state (after a controller
// restart). The file is scanned from the start — recordings are
// bounded at 100 MB and this is the cold path.
func ReadPage(path string, offset int64, limit int) (rows []PacketRow, total int64, err error) {
	err = scanRecording(path, func(index int64, p Packet) {
		if index >= offset && len(rows) < limit {
			rows = append(rows, p.Row)
		}

		total++
	})
	if err != nil {
		return nil, 0, err
	}

	return rows, total, nil
}

// ReadPacket reads one packet by index from a recording file.
func ReadPacket(path string, index int64) (Packet, error) {
	var (
		out   Packet
		found bool
	)

	err := scanRecording(path, func(i int64, p Packet) {
		if i == index {
			out = p
			found = true
		}
	})
	if err != nil {
		return Packet{}, err
	}

	if !found {
		return Packet{}, fmt.Errorf("packet %d not in recording", index)
	}

	return out, nil
}

// scanRecording walks every packet of a recording in order. A
// truncated tail (controller or capture died mid-write) ends the scan
// quietly: everything before it is intact.
func scanRecording(path string, fn func(index int64, p Packet)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrRecordingGone
		}

		return fmt.Errorf("open recording: %w", err)
	}

	defer f.Close() //nolint:errcheck // read-only

	r, err := pcapgo.NewNgReader(f, pcapgo.DefaultNgReaderOptions)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil // empty recording: header never made it
		}

		return fmt.Errorf("read pcapng header: %w", err)
	}

	for index := int64(0); ; index++ {
		data, ci, opts, err := r.ReadPacketDataWithOptions()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}

			return fmt.Errorf("read pcapng stream: %w", err)
		}

		fn(index, Packet{
			Row: PacketRow{
				Index:         index,
				Ts:            ci.Timestamp,
				Direction:     direction(opts),
				CaptureLength: ci.CaptureLength,
				WireLength:    ci.Length,
				Summary:       Summarize(data),
			},
			Data: data,
		})
	}
}
