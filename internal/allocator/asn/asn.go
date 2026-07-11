// Package asn implements the per-role ASN allocator.
package asn

import (
	"fmt"
	"sync"

	"github.com/ifantsai/dcnetlab/internal/model"
)

// Allocator hands out unique ASNs from per-role ranges.
type Allocator struct {
	mu     sync.Mutex
	ranges map[model.NodeRole]*roleRange
}

type roleRange struct {
	start, end uint32
	next       uint32
	allocated  map[uint32]string // asn -> owner
	released   []uint32
}

// New creates an allocator from the given role ranges.
func New(ranges []model.ASNRange) (*Allocator, error) {
	a := &Allocator{ranges: make(map[model.NodeRole]*roleRange)}
	for _, r := range ranges {
		if r.End < r.Start {
			return nil, fmt.Errorf("asn range for %s: end %d < start %d", r.Role, r.End, r.Start)
		}

		if _, dup := a.ranges[r.Role]; dup {
			return nil, fmt.Errorf("asn range for %s declared twice", r.Role)
		}

		a.ranges[r.Role] = &roleRange{
			start:     r.Start,
			end:       r.End,
			next:      r.Start,
			allocated: make(map[uint32]string),
		}
	}

	return a, nil
}

// Allocate returns the next free ASN for role.
func (a *Allocator) Allocate(role model.NodeRole, owner string) (uint32, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.ranges[role]
	if !ok {
		return 0, fmt.Errorf("no asn range for role %s", role)
	}

	if n := len(r.released); n > 0 {
		asn := r.released[n-1]
		r.released = r.released[:n-1]
		r.allocated[asn] = owner

		return asn, nil
	}

	if r.next > r.end {
		return 0, fmt.Errorf("asn range for role %s exhausted (%d-%d)", role, r.start, r.end)
	}

	asn := r.next
	r.next++
	r.allocated[asn] = owner

	return asn, nil
}

// Release returns an ASN to its role range.
func (a *Allocator) Release(role model.NodeRole, asn uint32) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.ranges[role]
	if !ok {
		return fmt.Errorf("no asn range for role %s", role)
	}

	if _, ok := r.allocated[asn]; !ok {
		return fmt.Errorf("asn %d not allocated in role %s", asn, role)
	}

	delete(r.allocated, asn)
	r.released = append(r.released, asn)

	return nil
}

// Restore marks an ASN as already allocated when reloading state.
func (a *Allocator) Restore(role model.NodeRole, asn uint32, owner string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.ranges[role]
	if !ok {
		return fmt.Errorf("no asn range for role %s", role)
	}

	if asn < r.start || asn > r.end {
		return fmt.Errorf("asn %d outside range %d-%d for role %s", asn, r.start, r.end, role)
	}

	if prev, ok := r.allocated[asn]; ok {
		return fmt.Errorf("asn %d already allocated to %s", asn, prev)
	}

	r.allocated[asn] = owner
	if asn >= r.next {
		r.next = asn + 1
	}

	return nil
}
