package main

// Linux native layer (experimental): /proc and /sys for routing, counters and CPU, nl80211 for WiFi,
// NetworkManager (D-Bus) for the metered flag, wpa_supplicant's journal lines for disconnect reasons.

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mdlayher/wifi"
)

const browserCmd = "xdg-open"

// appDataBase is where the data folder lives: $XDG_DATA_HOME, default ~/.local/share.
func appDataBase() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share")
}

// bestRoute returns the default gateway and its interface. dst is not needed: the probes only target the Internet.
func bestRoute(dst net.IP) (gw net.IP, ifIndex uint32, err error) {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil, 0, err
	}
	gw, name, err := parseProcRoute(string(b))
	if err != nil {
		return nil, 0, err
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, 0, err
	}
	return gw, uint32(ifi.Index), nil
}

// ifEntry reads the adapter byte counters from /sys/class/net; WiFi adapters have a "wireless" entry.
func ifEntry(ifIndex uint32) (in, out uint64, ifType uint32, err error) {
	ifi, err := net.InterfaceByIndex(int(ifIndex))
	if err != nil {
		return 0, 0, 0, err
	}
	dir := filepath.Join("/sys/class/net", ifi.Name)
	read := func(name string) uint64 {
		b, _ := os.ReadFile(filepath.Join(dir, "statistics", name))
		v, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		return v
	}
	ifType = 6 // ethernet
	if _, err := os.Stat(filepath.Join(dir, "wireless")); err == nil {
		ifType = ifTypeIEEE80211
	}
	return read("rx_bytes"), read("tx_bytes"), ifType, nil
}

func cpuTimes() (idle, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return parseProcStat(string(b))
}

// ---------- WiFi (nl80211, no root needed for the connected station) ----------

type wlan struct{ c *wifi.Client }

func openWlan() (*wlan, error) {
	c, err := wifi.New()
	if err != nil {
		return nil, err
	}
	return &wlan{c}, nil
}

func (w *wlan) info() (WifiInfo, error) {
	ifis, err := w.c.Interfaces()
	if err != nil {
		return WifiInfo{}, err
	}
	var wi WifiInfo
	for _, ifi := range ifis {
		if ifi.Type != wifi.InterfaceTypeStation || ifi.Name == "" {
			continue
		}
		wi = WifiInfo{Present: true, Iface: ifi.Name}
		bss, err := w.c.BSS(ifi)
		if err != nil || bss == nil || bss.Status != wifi.BSSStatusAssociated {
			continue // look for another adapter that is connected
		}
		wi.Connected, wi.State = true, 1
		wi.SSID, wi.BSSID = bss.SSID, bss.BSSID.String()
		wi.Channel, wi.Band = channelOfMHz(bss.Frequency), bandOfMHz(bss.Frequency)
		if st, err := w.c.StationInfo(ifi); err == nil && len(st) > 0 {
			wi.RSSI = float64(st[0].Signal)
			wi.RxMbps, wi.TxMbps = float64(st[0].ReceiveBitrate)/1e6, float64(st[0].TransmitBitrate)/1e6
		}
		wi.Quality = qualityOf(wi.RSSI)
		return wi, nil
	}
	return wi, nil
}

// ---------- metered, disconnect reason, notifications ----------

// isMetered asks NetworkManager. Metered: 1 yes, 2 no, 3 guess yes, 4 guess no, 0 unknown.
// lightBar: shortcut: assumes a dark panel (GNOME, KDE and most themes); there is no portable way to ask.
func lightBar() bool { return false }

func isMetered() (metered, ok bool) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return false, false
	}
	defer conn.Close()
	v, err := conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager").
		GetProperty("org.freedesktop.NetworkManager.Metered")
	if err != nil {
		return false, false
	}
	switch m, _ := v.Value().(uint32); m {
	case 1, 3:
		return true, true
	case 2, 4:
		return false, true
	}
	return false, false
}

// lastDisconnectReason reads wpa_supplicant's last disconnect line from the journal
// (readable by users in the systemd-journal or adm group, the default for the first desktop user).
func lastDisconnectReason(maxAge time.Duration) (code int, reason string, found bool) {
	out, err := exec.Command("journalctl", "--no-pager", "-q", "-o", "cat",
		"--since", fmt.Sprintf("%d seconds ago", int(maxAge.Seconds()))).Output()
	if err != nil {
		return 0, "", false
	}
	return parseDisconnectLog(string(out))
}

// toast uses the freedesktop notification service, present on every mainstream desktop.
func toast(title, body string) {
	go func() {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
			"org.freedesktop.Notifications.Notify", 0, appName, uint32(0), "network-wireless", title, body,
			[]string{}, map[string]dbus.Variant{}, int32(-1))
	}()
}

// ---------- autostart: XDG autostart entry ----------

func autostartFile() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "autostart", "isthisconnected.desktop")
}

func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(autostartFile()), 0o755); err != nil {
		return err
	}
	entry := "[Desktop Entry]\nType=Application\nName=" + appName + "\nExec=" + strconv.Quote(exe) + "\nX-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(autostartFile(), []byte(entry), 0o644)
}

func uninstallAutostart() error { return os.Remove(autostartFile()) }

// shortcut: no AP scan on Linux (triggering an nl80211 scan needs CAP_NET_ADMIN); read the cached BSS dump if asked.
func (w *wlan) triggerScan()         {}
func (w *wlan) scan() ([]BSS, error) { return nil, nil }
