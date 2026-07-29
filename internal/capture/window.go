package capture

import "time"

// PacketRow is one packet's metadata as shown in the packet list; the
// JSON form goes out on the capture WebSocket verbatim.
type PacketRow struct {
	Index         int64     `json:"index"`
	Ts            time.Time `json:"ts"`
	Direction     string    `json:"direction,omitempty"`
	CaptureLength int       `json:"captureLength"`
	WireLength    int       `json:"wireLength"`
	Summary
}

// Packet is one captured packet held in the live window: its list row
// plus the snapped raw bytes for the detail view.
type Packet struct {
	Row  PacketRow
	Data []byte
}

// window keeps the most recent packets of a session in a fixed-size
// ring. Older packets fall out of the live view (the pcapng file
// keeps all of them); total and firstAvailable let readers detect
// that.
type window struct {
	buf   []Packet
	size  int
	total int64
}

func newWindow(size int) *window {
	return &window{buf: make([]Packet, 0, size), size: size}
}

func (w *window) append(p Packet) {
	if len(w.buf) == w.size {
		copy(w.buf, w.buf[1:])
		w.buf[len(w.buf)-1] = p
	} else {
		w.buf = append(w.buf, p)
	}

	w.total++
}

// first returns the oldest index still held.
func (w *window) first() int64 {
	return w.total - int64(len(w.buf))
}

// get returns the packet at the given capture index if it is still in
// the window.
func (w *window) get(index int64) (Packet, bool) {
	first := w.first()
	if index < first || index >= w.total {
		return Packet{}, false
	}

	return w.buf[index-first], true
}

// page returns up to limit packets starting at offset (clamped into
// the window).
func (w *window) page(offset int64, limit int) []Packet {
	first := w.first()
	if offset < first {
		offset = first
	}

	if offset >= w.total || limit <= 0 {
		return nil
	}

	end := min(offset+int64(limit), w.total)
	out := make([]Packet, end-offset)
	copy(out, w.buf[offset-first:end-first])

	return out
}
