// Package ipam implements the address pool allocator. Every address
// used in the fabric must come from a pool; templates never compute
// addresses themselves.
package ipam

import (
	"fmt"
	"net/netip"
	"sync"
)

// Pool hands out fixed-size subnets from one CIDR in order, reusing
// released subnets before extending the high-water mark.
type Pool struct {
	mu     sync.Mutex
	name   string
	cidr   netip.Prefix
	prefix int // allocation prefix length, e.g. 31 for p2p links

	next      netip.Addr              // first address of the next never-used subnet
	allocated map[netip.Prefix]string // subnet -> owner
	released  []netip.Prefix
}

// NewPool creates a pool over cidr that allocates subnets of length
// allocPrefix. allocPrefix must not be shorter than the pool prefix.
func NewPool(name, cidr string, allocPrefix int) (*Pool, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("ipam pool %s: %w", name, err)
	}

	p = p.Masked()
	if allocPrefix < p.Bits() || allocPrefix > p.Addr().BitLen() {
		return nil, fmt.Errorf("ipam pool %s: allocation prefix /%d outside pool %s", name, allocPrefix, p)
	}

	return &Pool{
		name:      name,
		cidr:      p,
		prefix:    allocPrefix,
		next:      p.Addr(),
		allocated: make(map[netip.Prefix]string),
	}, nil
}

// Name returns the pool name.
func (p *Pool) Name() string { return p.name }

// CIDR returns the pool CIDR.
func (p *Pool) CIDR() netip.Prefix { return p.cidr }

// Allocate returns the next free subnet and records its owner.
func (p *Pool) Allocate(owner string) (netip.Prefix, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if n := len(p.released); n > 0 {
		sub := p.released[n-1]
		p.released = p.released[:n-1]
		p.allocated[sub] = owner

		return sub, nil
	}

	sub := netip.PrefixFrom(p.next, p.prefix)
	if !p.cidr.Contains(sub.Addr()) || sub.Masked().Addr() != sub.Addr() {
		return netip.Prefix{}, fmt.Errorf("ipam pool %s (%s): exhausted", p.name, p.cidr)
	}

	p.next = nextSubnetStart(sub)
	p.allocated[sub] = owner

	return sub, nil
}

// Release returns a subnet to the pool for reuse.
func (p *Pool) Release(sub netip.Prefix) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.allocated[sub]; !ok {
		return fmt.Errorf("ipam pool %s: %s not allocated", p.name, sub)
	}

	delete(p.allocated, sub)
	p.released = append(p.released, sub)

	return nil
}

// Restore marks a subnet as already allocated, used when reloading
// state from the store on startup.
func (p *Pool) Restore(sub netip.Prefix, owner string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sub.Bits() != p.prefix || !p.cidr.Contains(sub.Addr()) {
		return fmt.Errorf("ipam pool %s: %s does not belong to pool %s/%d", p.name, sub, p.cidr, p.prefix)
	}

	if prev, ok := p.allocated[sub]; ok {
		return fmt.Errorf("ipam pool %s: %s already allocated to %s", p.name, sub, prev)
	}

	p.allocated[sub] = owner
	if end := nextSubnetStart(sub); end.Compare(p.next) > 0 {
		p.next = end
	}

	return nil
}

// Usage returns allocated and total subnet counts.
func (p *Pool) Usage() (allocated int, total uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	bits := p.prefix - p.cidr.Bits()
	if bits >= 63 {
		return len(p.allocated), 1 << 63
	}

	return len(p.allocated), 1 << bits
}

// nextSubnetStart returns the first address after subnet sub,
// computed by adding the subnet size to its base address.
func nextSubnetStart(sub netip.Prefix) netip.Addr {
	b := sub.Masked().Addr().As16()
	hostBits := sub.Addr().BitLen() - sub.Bits()
	// Add 2^hostBits: set the carry at byte position from the right.
	bitPos := hostBits
	for i := 15; i >= 0 && bitPos >= 0; i-- {
		var add uint16
		if bitPos < 8 {
			add = 1 << bitPos
		}

		bitPos -= 8
		if add == 0 {
			continue
		}

		sum := uint16(b[i]) + add
		b[i] = byte(sum)
		// propagate carry
		for j := i - 1; sum > 0xff && j >= 0; j-- {
			sum = uint16(b[j]) + 1
			b[j] = byte(sum)
		}

		break
	}

	a := netip.AddrFrom16(b)
	if sub.Addr().Is4() {
		return a.Unmap()
	}

	return a
}
