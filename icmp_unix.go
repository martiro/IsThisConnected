//go:build linux || darwin

package main

// ICMP on Linux and macOS through unprivileged "ping sockets" (SOCK_DGRAM + IPPROTO_ICMP): no root needed.
// On Linux they are allowed when the user's group is in net.ipv4.ping_group_range (the default on major distros).

import (
	"fmt"
	"math/rand/v2"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

type icmp struct {
	fd  int
	id  uint16
	seq uint16
}

func newICMP() (*icmp, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_ICMP)
	if err != nil {
		return nil, fmt.Errorf("ICMP ping socket: %w (on Linux check: sysctl net.ipv4.ping_group_range)", err)
	}
	if err := icmpSetup(fd); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &icmp{fd: fd, id: uint16(rand.Uint32())}, nil
}

func (c *icmp) close() { unix.Close(c.fd) }

// ping returns RTT in ms; ok=false on timeout/unreachable.
func (c *icmp) ping(ip net.IP, timeoutMs uint32) (rtt float64, ok bool) {
	_, rtt, ok = c.echo(ip, 0, timeoutMs)
	return
}

// echo sends one ICMP echo; ttl 0 = default (64). from is who answered: the target (reached=true) or,
// with a small TTL, the router where the packet expired (traceroute hop).
func (c *icmp) echo(ip net.IP, ttl byte, timeoutMs uint32) (from net.IP, rtt float64, reached bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, 0, false
	}
	if ttl == 0 {
		ttl = 64
	}
	if unix.SetsockoptInt(c.fd, unix.IPPROTO_IP, unix.IP_TTL, int(ttl)) != nil {
		return nil, 0, false
	}
	c.seq++
	start := time.Now()
	if unix.Sendto(c.fd, echoRequest(c.id, c.seq), 0, &unix.SockaddrInet4{Addr: [4]byte(ip4)}) != nil {
		return nil, 0, false
	}
	ms := func() float64 { return float64(time.Since(start).Microseconds()) / 1000 }
	deadline := start.Add(time.Duration(timeoutMs) * time.Millisecond)
	buf := make([]byte, 1500)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, 0, false
		}
		fds := []unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
		if err == unix.EINTR {
			continue
		}
		if n <= 0 {
			return nil, 0, false
		}
		if fds[0].Revents&unix.POLLERR != 0 { // Linux reports ICMP errors (time exceeded, unreachable) on the error queue
			if hop, mine := readErrQueue(c.fd, c.seq); mine {
				return hop, ms(), false
			}
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		nr, sa, err := unix.Recvfrom(c.fd, buf, unix.MSG_DONTWAIT)
		src, isV4 := sa.(*unix.SockaddrInet4)
		if err != nil || !isV4 {
			continue
		}
		srcIP := net.IPv4(src.Addr[0], src.Addr[1], src.Addr[2], src.Addr[3]).To4()
		switch typ, ok := parseReply(buf[:nr], c.id, c.seq, !kernelSetsICMPID); {
		case ok && typ == icmpEchoReply && srcIP.Equal(ip4):
			return srcIP, ms(), true
		case ok && typ == icmpTimeExceeded:
			return srcIP, ms(), false
		}
	}
}
