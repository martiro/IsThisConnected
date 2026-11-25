package main

// Native Windows calls: ICMP, routing, interface counters, WLAN, CPU, single instance, hidden exec.

import (
	"encoding/binary"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi          = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreate    = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpClose     = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho2 = iphlpapi.NewProc("IcmpSendEcho2")
	procGetBestRoute  = iphlpapi.NewProc("GetBestRoute")
	procGetIfEntry2   = iphlpapi.NewProc("GetIfEntry2")

	procGetSystemTimes = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemTimes")

	wlanapi         = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpen    = wlanapi.NewProc("WlanOpenHandle")
	procWlanEnum    = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQuery   = wlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMem = wlanapi.NewProc("WlanFreeMemory")
	procWlanBssList = wlanapi.NewProc("WlanGetNetworkBssList")
)

func ip4u32(ip net.IP) uint32 { return binary.LittleEndian.Uint32(ip.To4()) } // network byte order in memory

func u32ip(v uint32) net.IP {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return net.IP(b)
}

// ---------- ICMP (IcmpSendEcho2, no admin needed) ----------

type icmp struct{ h uintptr }

func newICMP() (*icmp, error) {
	h, _, err := procIcmpCreate.Call()
	if h == 0 || h == uintptr(windows.InvalidHandle) {
		return nil, err
	}
	return &icmp{h}, nil
}

func (c *icmp) close() { procIcmpClose.Call(c.h) }

// ping returns RTT in ms; ok=false on timeout/unreachable.
func (c *icmp) ping(ip net.IP, timeoutMs uint32) (rtt float64, ok bool) {
	_, rtt, ok = c.echo(ip, 0, timeoutMs)
	return
}

const ipTTLExpiredTransit = 11013

// echo sends one ICMP echo; ttl 0 = system default. from is who answered: the target (reached=true) or,
// with a small TTL, the router where the packet expired (this is how traceroute finds the hops).
func (c *icmp) echo(ip net.IP, ttl byte, timeoutMs uint32) (from net.IP, rtt float64, reached bool) {
	data := []byte("isthisconnected-probe-payload-32")
	reply := make([]byte, 256) // > sizeof(ICMP_ECHO_REPLY)=40 + payload + 8
	var opt [16]byte           // IP_OPTION_INFORMATION on x64: Ttl, Tos, Flags, OptionsSize, padding, OptionsData*
	var optp uintptr
	if ttl > 0 {
		opt[0] = ttl
		optp = uintptr(unsafe.Pointer(&opt[0]))
	}
	n, _, _ := procIcmpSendEcho2.Call(c.h, 0, 0, 0, uintptr(ip4u32(ip)),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), optp,
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeoutMs))
	if n == 0 {
		return nil, 0, false
	}
	status := binary.LittleEndian.Uint32(reply[4:8])
	if status != 0 && status != ipTTLExpiredTransit {
		return nil, 0, false
	}
	return u32ip(binary.LittleEndian.Uint32(reply[0:4])), float64(binary.LittleEndian.Uint32(reply[8:12])), status == 0
}

// cpuTimes returns idle and total CPU time (100 ns units) since boot; kernel time already includes idle.
func cpuTimes() (idle, total uint64, ok bool) {
	var i, k, u windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	return ft(i), ft(k) + ft(u), r != 0
}

// ---------- routing ----------

// bestRoute returns the next hop (gateway) and interface index used to reach dst.
func bestRoute(dst net.IP) (gw net.IP, ifIndex uint32, err error) {
	var row [14]uint32 // MIB_IPFORWARDROW
	r, _, _ := procGetBestRoute.Call(uintptr(ip4u32(dst)), 0, uintptr(unsafe.Pointer(&row[0])))
	if r != 0 {
		return nil, 0, syscall.Errno(r)
	}
	return u32ip(row[3]), row[4], nil
}

// ifEntry reads 64-bit in/out byte counters and the interface type via GetIfEntry2 (MIB_IF_ROW2, 1352 bytes).
func ifEntry(ifIndex uint32) (in, out uint64, ifType uint32, err error) {
	var row [169]uint64 // 1352 bytes, 8-aligned
	b := (*[1352]byte)(unsafe.Pointer(&row[0]))
	binary.LittleEndian.PutUint32(b[8:], ifIndex)
	r, _, _ := procGetIfEntry2.Call(uintptr(unsafe.Pointer(&row[0])))
	if r != 0 {
		return 0, 0, 0, syscall.Errno(r)
	}
	return binary.LittleEndian.Uint64(b[1208:]), binary.LittleEndian.Uint64(b[1280:]), binary.LittleEndian.Uint32(b[1128:]), nil
}

// ---------- WLAN ----------

type wlan struct {
	h               uintptr
	bandBSSID, band string
}

func openWlan() (*wlan, error) {
	var ver uint32
	var h uintptr
	r, _, _ := procWlanOpen.Call(2, 0, uintptr(unsafe.Pointer(&ver)), uintptr(unsafe.Pointer(&h)))
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return &wlan{h: h}, nil
}

const (
	opcodeCurrentConnection = 7
	opcodeChannelNumber     = 8
	opcodeRSSI              = 0x10000102
	wlanStateConnected      = 1
)

var phyNames = map[uint32]string{4: "802.11a", 5: "802.11b", 6: "802.11g", 7: "802.11n", 8: "802.11ac", 10: "802.11ax", 11: "802.11be"}

func (w *wlan) query(guid *windows.GUID, opcode uint32) ([]byte, error) {
	var size uint32
	var data unsafe.Pointer // allocated by WLAN, freed with WlanFreeMemory
	var vt int32
	r, _, _ := procWlanQuery.Call(w.h, uintptr(unsafe.Pointer(guid)), uintptr(opcode), 0,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&vt)))
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	defer procWlanFreeMem.Call(uintptr(data))
	return append([]byte(nil), unsafe.Slice((*byte)(data), size)...), nil
}

func (w *wlan) info() (WifiInfo, error) {
	var list unsafe.Pointer
	r, _, _ := procWlanEnum.Call(w.h, 0, uintptr(unsafe.Pointer(&list)))
	if r != 0 {
		return WifiInfo{}, syscall.Errno(r)
	}
	n := *(*uint32)(list)
	if n == 0 {
		procWlanFreeMem.Call(uintptr(list))
		return WifiInfo{}, nil
	}
	// WLAN_INTERFACE_INFO: GUID(16) + WCHAR[256] + state(4) = 532 bytes, list header 8 bytes
	raw := append([]byte(nil), unsafe.Slice((*byte)(unsafe.Add(list, 8)), 532*int(n))...)
	procWlanFreeMem.Call(uintptr(list))
	pick := 0
	for i := 0; i < int(n); i++ {
		if binary.LittleEndian.Uint32(raw[i*532+528:]) == wlanStateConnected {
			pick = i
			break
		}
	}
	it := raw[pick*532 : (pick+1)*532]
	guid := *(*windows.GUID)(unsafe.Pointer(&it[0]))
	wi := WifiInfo{Present: true, State: int(binary.LittleEndian.Uint32(it[528:])), Iface: utf16z(it[16:528])}
	if wi.State != wlanStateConnected {
		return wi, nil
	}
	cc, err := w.query(&guid, opcodeCurrentConnection)
	if err != nil || len(cc) < 588 {
		return wi, nil // transient: state changed between calls
	}
	wi.Connected = true
	ssidLen := binary.LittleEndian.Uint32(cc[520:])
	if ssidLen > 32 {
		ssidLen = 32
	}
	wi.SSID = string(cc[524 : 524+ssidLen])
	wi.BSSID = net.HardwareAddr(cc[560:566]).String()
	wi.Phy = phyNames[binary.LittleEndian.Uint32(cc[568:])]
	wi.Quality = float64(binary.LittleEndian.Uint32(cc[576:]))
	wi.RxMbps = float64(binary.LittleEndian.Uint32(cc[580:])) / 1000
	wi.TxMbps = float64(binary.LittleEndian.Uint32(cc[584:])) / 1000
	if b, err := w.query(&guid, opcodeRSSI); err == nil && len(b) >= 4 {
		wi.RSSI = float64(int32(binary.LittleEndian.Uint32(b)))
	} else {
		wi.RSSI = wi.Quality/2 - 100 // documented approximation of quality->dBm
	}
	if b, err := w.query(&guid, opcodeChannelNumber); err == nil && len(b) >= 4 {
		wi.Channel = int(binary.LittleEndian.Uint32(b))
	}
	if wi.BSSID != w.bandBSSID { // channel numbers overlap between 5 and 6 GHz: read the real frequency once per BSSID
		w.bandBSSID, w.band = wi.BSSID, w.bandOf(&guid, cc[560:566])
	}
	wi.Band = w.band
	return wi, nil
}

// bandOf looks up the BSS center frequency in the driver's cached BSS list (no scan triggered).
func (w *wlan) bandOf(guid *windows.GUID, bssid []byte) string {
	var list unsafe.Pointer
	r, _, _ := procWlanBssList.Call(w.h, uintptr(unsafe.Pointer(guid)), 0, 3 /*dot11_BSS_type_any*/, 0, 0, uintptr(unsafe.Pointer(&list)))
	if r != 0 {
		return ""
	}
	defer procWlanFreeMem.Call(uintptr(list))
	n := int(*(*uint32)(unsafe.Add(list, 4)))
	const entry = 360 // sizeof(WLAN_BSS_ENTRY)
	raw := unsafe.Slice((*byte)(unsafe.Add(list, 8)), entry*n)
	for i := 0; i < n; i++ {
		e := raw[i*entry:]
		if string(e[40:46]) == string(bssid) {
			switch f := binary.LittleEndian.Uint32(e[92:]); { // kHz
			case f >= 5925000:
				return "6 GHz"
			case f >= 4900000:
				return "5 GHz"
			default:
				return "2.4 GHz"
			}
		}
	}
	return ""
}

func utf16z(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return windows.UTF16ToString(u)
}

// ---------- misc ----------

var instanceMutex windows.Handle

func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\IsThisConnected-single-instance`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return false
	}
	instanceMutex = h
	return true
}

func hiddenCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

func msgBox(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(appName)
	windows.MessageBox(0, t, c, 0x40)
}

func openBrowser() {
	hiddenCmd("rundll32", "url.dll,FileProtocolHandler", "http://"+listenAddr).Start()
}
