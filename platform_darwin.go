package main

// macOS native layer (experimental): CoreWLAN for WiFi, Mach host statistics for CPU, getifaddrs for the
// adapter counters, the routing socket for the default gateway. Needs cgo (the tray needs it too).
// Since macOS 14 the network name and BSSID are only returned to apps the user allowed to use Location;
// a bare executable (not an .app bundle) cannot ask for it, so expect them to be hidden.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework CoreWLAN -framework Foundation
#import <CoreWLAN/CoreWLAN.h>
#include <mach/mach.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	int present, connected, rssi, channel, band, phy;
	double txRate;
	char iface[32], ssid[128], bssid[32];
} itc_wifi;

static itc_wifi itc_wifi_info(void) {
	itc_wifi w;
	memset(&w, 0, sizeof w);
	@autoreleasepool {
		CWInterface *i = [[CWWiFiClient sharedWiFiClient] interface];
		if (i == nil) return w;
		w.present = 1;
		if ([i interfaceName]) strlcpy(w.iface, [[i interfaceName] UTF8String], sizeof w.iface);
		CWChannel *c = [i wlanChannel]; // nil when not associated
		if (![i powerOn] || c == nil) return w;
		w.connected = 1;
		if ([i ssid]) strlcpy(w.ssid, [[i ssid] UTF8String], sizeof w.ssid);
		if ([i bssid]) strlcpy(w.bssid, [[i bssid] UTF8String], sizeof w.bssid);
		w.rssi = (int)[i rssiValue];
		w.txRate = [i transmitRate];
		w.channel = (int)[c channelNumber];
		w.band = (int)[c channelBand];
		w.phy = (int)[i activePHYMode];
	}
	return w;
}

static int itc_cpu(unsigned long long *idle, unsigned long long *total) {
	host_cpu_load_info_data_t info;
	mach_msg_type_number_t n = HOST_CPU_LOAD_INFO_COUNT;
	if (host_statistics(mach_host_self(), HOST_CPU_LOAD_INFO, (host_info_t)&info, &n) != KERN_SUCCESS) return 0;
	*idle = info.cpu_ticks[CPU_STATE_IDLE];
	*total = (unsigned long long)info.cpu_ticks[CPU_STATE_USER] + info.cpu_ticks[CPU_STATE_SYSTEM] +
		info.cpu_ticks[CPU_STATE_IDLE] + info.cpu_ticks[CPU_STATE_NICE];
	return 1;
}

static int itc_if_bytes(const char *name, unsigned long long *in, unsigned long long *out) {
	struct ifaddrs *list, *p;
	int found = 0;
	if (getifaddrs(&list) != 0) return 0;
	for (p = list; p; p = p->ifa_next) {
		if (p->ifa_addr && p->ifa_addr->sa_family == AF_LINK && p->ifa_data && strcmp(p->ifa_name, name) == 0) {
			struct if_data *d = (struct if_data *)p->ifa_data;
			*in = d->ifi_ibytes; // 32-bit counters: a wrap just drops one throughput sample
			*out = d->ifi_obytes;
			found = 1;
			break;
		}
	}
	freeifaddrs(list);
	return found;
}
*/
import "C"

import (
	"fmt"
	"html"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/route"
)

const browserCmd = "open"

// appDataBase is where the data folder lives: ~/Library/Application Support.
func appDataBase() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "Application Support")
}

// bestRoute returns the default gateway from the routing table (the unscoped default route).
func bestRoute(dst net.IP) (gw net.IP, ifIndex uint32, err error) {
	rib, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, 0, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, 0, err
	}
	const rtfIfscope = 0x1000000 // per-interface copies of the default route: skip them
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || rm.Flags&syscall.RTF_GATEWAY == 0 || rm.Flags&rtfIfscope != 0 || len(rm.Addrs) <= syscall.RTAX_NETMASK {
			continue
		}
		d, ok := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		if !ok || d.IP != [4]byte{} {
			continue
		}
		if mask, ok := rm.Addrs[syscall.RTAX_NETMASK].(*route.Inet4Addr); ok && mask.IP != [4]byte{} {
			continue
		}
		if g, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr); ok {
			return net.IPv4(g.IP[0], g.IP[1], g.IP[2], g.IP[3]).To4(), uint32(rm.Index), nil
		}
	}
	return nil, 0, fmt.Errorf("no default route")
}

var wifiIface = sync.OnceValue(func() string { // the WiFi adapter's BSD name, e.g. en0
	w := C.itc_wifi_info()
	return C.GoString(&w.iface[0])
})

// ifEntry reads the adapter byte counters. macOS reports WiFi as Ethernet, so the WiFi adapter is
// recognized by name.
func ifEntry(ifIndex uint32) (in, out uint64, ifType uint32, err error) {
	ifi, err := net.InterfaceByIndex(int(ifIndex))
	if err != nil {
		return 0, 0, 0, err
	}
	name := C.CString(ifi.Name)
	defer C.free(unsafe.Pointer(name))
	var i, o C.ulonglong
	if C.itc_if_bytes(name, &i, &o) == 0 {
		return 0, 0, 0, fmt.Errorf("no counters for %s", ifi.Name)
	}
	ifType = 6
	if ifi.Name == wifiIface() {
		ifType = ifTypeIEEE80211
	}
	return uint64(i), uint64(o), ifType, nil
}

func cpuTimes() (idle, total uint64, ok bool) {
	var i, t C.ulonglong
	if C.itc_cpu(&i, &t) == 0 {
		return 0, 0, false
	}
	return uint64(i), uint64(t), true
}

// ---------- WiFi (CoreWLAN) ----------

type wlan struct{}

func openWlan() (*wlan, error) { return &wlan{}, nil }

var darwinPHY = map[int]string{1: "802.11a", 2: "802.11b", 3: "802.11g", 4: "802.11n", 5: "802.11ac", 6: "802.11ax", 7: "802.11be"}

func (w *wlan) info() (WifiInfo, error) {
	c := C.itc_wifi_info()
	wi := WifiInfo{Present: c.present != 0, Connected: c.connected != 0, Iface: C.GoString(&c.iface[0])}
	if !wi.Connected {
		return wi, nil
	}
	wi.State = 1
	wi.SSID, wi.BSSID = C.GoString(&c.ssid[0]), C.GoString(&c.bssid[0])
	if wi.SSID == "" {
		wi.SSID = "(hidden by macOS)" // macOS 14+ hides it from apps without Location permission
	}
	wi.RSSI = float64(c.rssi)
	wi.Quality = qualityOf(wi.RSSI)
	wi.RxMbps, wi.TxMbps = float64(c.txRate), float64(c.txRate) // CoreWLAN only exposes the transmit rate
	wi.Channel = int(c.channel)
	wi.Band = map[int]string{1: "2.4 GHz", 2: "5 GHz", 3: "6 GHz"}[int(c.band)]
	wi.Phy = darwinPHY[int(c.phy)]
	return wi, nil
}

// ---------- not available on macOS (no public API) ----------

func isMetered() (metered, ok bool) { return false, false }

var (
	barMu    sync.Mutex
	barLight bool
	barAt    time.Time
)

// lightBar reports whether the menu bar is light (no AppleInterfaceStyle = light mode). Cached for a minute:
// it runs a process and the tray asks every few seconds.
func lightBar() bool {
	barMu.Lock()
	defer barMu.Unlock()
	if time.Since(barAt) > time.Minute {
		out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
		barLight, barAt = err != nil || !strings.Contains(string(out), "Dark"), time.Now()
	}
	return barLight
}

func lastDisconnectReason(maxAge time.Duration) (code int, reason string, found bool) {
	return 0, "", false
}

func toast(title, body string) {
	exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title %q", body, title)).Start()
}

// ---------- autostart: LaunchAgent ----------

const launchLabel = "com.github.martiro.isthisconnected"

func launchAgentFile() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", launchLabel+".plist")
}

func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>Label</key><string>` + launchLabel + `</string>
	<key>ProgramArguments</key><array><string>` + html.EscapeString(exe) + `</string></array>
	<key>RunAtLoad</key><true/>
</dict></plist>
`
	if err := os.MkdirAll(filepath.Dir(launchAgentFile()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(launchAgentFile(), []byte(plist), 0o644)
}

func uninstallAutostart() error { return os.Remove(launchAgentFile()) }

// shortcut: no AP scan on macOS (CoreWLAN scanning needs location permission); add scanForNetworks if asked.
func (w *wlan) triggerScan()         {}
func (w *wlan) scan() ([]BSS, error) { return nil, nil }
