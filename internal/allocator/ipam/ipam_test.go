package ipam

import (
	"net/netip"
	"testing"
)

func TestAllocateSequential31(t *testing.T) {
	p, err := NewPool("fabricP2P", "10.0.0.0/12", 31)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"10.0.0.0/31", "10.0.0.2/31", "10.0.0.4/31"}
	for i, w := range want {
		got, err := p.Allocate("link")
		if err != nil {
			t.Fatal(err)
		}

		if got.String() != w {
			t.Errorf("alloc %d: got %s, want %s", i, got, w)
		}
	}
}

func TestAllocateLoopback32(t *testing.T) {
	p, err := NewPool("loopback", "10.255.0.0/16", 32)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := p.Allocate("n1")
	b, _ := p.Allocate("n2")
	if a.String() != "10.255.0.0/32" || b.String() != "10.255.0.1/32" {
		t.Errorf("got %s, %s", a, b)
	}
}

func TestReleaseAndReuse(t *testing.T) {
	p, _ := NewPool("t", "192.168.0.0/24", 31)
	a, _ := p.Allocate("x")
	b, _ := p.Allocate("y")
	if err := p.Release(a); err != nil {
		t.Fatal(err)
	}

	c, _ := p.Allocate("z")
	if c != a {
		t.Errorf("expected released subnet %s to be reused, got %s", a, c)
	}

	if err := p.Release(b); err != nil {
		t.Fatal(err)
	}

	if err := p.Release(b); err == nil {
		t.Error("double release should fail")
	}
}

func TestExhaustion(t *testing.T) {
	p, _ := NewPool("tiny", "10.0.0.0/30", 31)
	if _, err := p.Allocate("a"); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Allocate("b"); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Allocate("c"); err == nil {
		t.Error("expected exhaustion error")
	}
}

func TestCrossByteBoundary(t *testing.T) {
	p, _ := NewPool("t", "10.0.0.0/16", 24)
	a, _ := p.Allocate("a")
	b, _ := p.Allocate("b")
	if a.String() != "10.0.0.0/24" || b.String() != "10.0.1.0/24" {
		t.Errorf("got %s, %s", a, b)
	}
}

func TestRestore(t *testing.T) {
	p, _ := NewPool("t", "10.0.0.0/24", 31)
	sub := netip.MustParsePrefix("10.0.0.4/31")
	if err := p.Restore(sub, "owner"); err != nil {
		t.Fatal(err)
	}

	if err := p.Restore(sub, "other"); err == nil {
		t.Error("restore of allocated subnet should fail")
	}

	// next allocation must not collide and continues after restored subnet
	got, _ := p.Allocate("x")
	if got.String() != "10.0.0.6/31" {
		t.Errorf("got %s, want 10.0.0.6/31", got)
	}

	if err := p.Restore(netip.MustParsePrefix("11.0.0.0/31"), "bad"); err == nil {
		t.Error("restore outside pool should fail")
	}
}

func TestUsage(t *testing.T) {
	p, _ := NewPool("t", "10.0.0.0/24", 31)
	if _, err := p.Allocate("a"); err != nil {
		t.Fatal(err)
	}

	alloc, total := p.Usage()
	if alloc != 1 || total != 128 {
		t.Errorf("got %d/%d, want 1/128", alloc, total)
	}
}
