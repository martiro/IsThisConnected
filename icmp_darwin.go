package main

import "net"

// macOS keeps our echo id, and every ping socket of the process may see every ICMP reply: match id and seq.
const kernelSetsICMPID = false

// icmpSetup: nothing to do; macOS delivers ICMP errors as normal datagrams (handled by parseReply).
func icmpSetup(fd int) error { return nil }

// readErrQueue: macOS has no error queue.
func readErrQueue(fd int, seq uint16) (net.IP, bool) { return nil, false }
