package main

import (
	"encoding/binary"
	"net"

	"golang.org/x/sys/unix"
)

// The kernel replaces the echo id of ping-socket packets and routes replies to the right socket.
const kernelSetsICMPID = true

// icmpSetup asks for ICMP errors on the error queue: that is how a ping socket learns the traceroute hops.
func icmpSetup(fd int) error {
	return unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1)
}

// readErrQueue reads one queued ICMP error. mine=true when it belongs to probe seq;
// hop is the router that sent "time exceeded" (nil for other errors, e.g. unreachable).
func readErrQueue(fd int, seq uint16) (hop net.IP, mine bool) {
	buf := make([]byte, 512)
	oob := make([]byte, 512)
	n, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
	if err != nil {
		return nil, false
	}
	if n >= 8 && binary.BigEndian.Uint16(buf[6:8]) != seq { // the queued payload is our own echo request
		return nil, false
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, false
	}
	for _, m := range msgs {
		// struct sock_extended_err { u32 ee_errno; u8 ee_origin, ee_type, ee_code, ee_pad; u32 ee_info, ee_data; }
		// followed by the offender's struct sockaddr_in (family, port, address).
		if m.Header.Level != unix.IPPROTO_IP || m.Header.Type != unix.IP_RECVERR || len(m.Data) < 16+8 {
			continue
		}
		if m.Data[4] == unix.SO_EE_ORIGIN_ICMP && m.Data[5] == icmpTimeExceeded {
			return net.IP(append([]byte(nil), m.Data[20:24]...)), true
		}
		return nil, true
	}
	return nil, false
}
