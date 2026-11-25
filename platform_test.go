package main

import (
	"net"
	"testing"
	"time"
)

// Runs in CI on Windows, Linux and macOS (not skipped by -short): the native calls that work without WiFi.
func TestPlatformBasics(t *testing.T) {
	i1, t1, ok1 := cpuTimes()
	time.Sleep(200 * time.Millisecond)
	i2, t2, ok2 := cpuTimes()
	if !ok1 || !ok2 || t2 <= t1 || i2 < i1 {
		t.Errorf("cpuTimes not advancing: %d/%d -> %d/%d", i1, t1, i2, t2)
	}

	c, err := newICMP()
	if err != nil {
		t.Fatalf("ICMP: %v", err)
	}
	defer c.close()
	if rtt, ok := c.ping(net.ParseIP("127.0.0.1"), 1000); !ok {
		t.Error("no reply from 127.0.0.1")
	} else {
		t.Logf("loopback ping %.2f ms", rtt)
	}
	from, _, reached := c.echo(net.ParseIP("127.0.0.1"), 1, 1000) // TTL 1 is enough for loopback
	if !reached || !from.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("echo with TTL: from %v reached %v", from, reached)
	}

	gw, idx, err := bestRoute(net.ParseIP("8.8.8.8"))
	if err != nil {
		t.Skipf("no default route here: %v", err)
	}
	in, out, typ, err := ifEntry(idx)
	if err != nil {
		t.Fatalf("ifEntry(%d): %v", idx, err)
	}
	t.Logf("gateway %v ifIndex %d type %d in %d out %d", gw, idx, typ, in, out)
	if in == 0 && out == 0 {
		t.Error("adapter counters are zero on the default route")
	}
	// Informational only: cloud CI networks usually drop traceroute replies.
	hop, _, _ := c.echo(net.ParseIP("8.8.8.8"), 1, 1000)
	t.Logf("first hop toward 8.8.8.8: %v", hop)
}
