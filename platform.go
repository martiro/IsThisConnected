package main

// Platform-neutral pieces of the native layer. Each OS implements, in platform_<os>.go and friends:
//   newICMP / (*icmp).ping / (*icmp).echo / (*icmp).close   ping and one-hop traceroute probes
//   bestRoute, ifEntry, cpuTimes                            default gateway, adapter byte counters, CPU time
//   openWlan / (*wlan).info                                 current WiFi connection
//   isMetered, lastDisconnectReason, toast                  metered flag, why the WiFi dropped, notifications
//   installAutostart, uninstallAutostart, appDataBase      start at sign-in, data folder
//   singleInstance, msgBox, openBrowser                     process and UI helpers

// ifTypeIEEE80211 is the interface type reported for WiFi adapters (IANA ifType 71).
const ifTypeIEEE80211 = 71

type WifiInfo struct {
	Present   bool    `json:"present"`
	Connected bool    `json:"connected"`
	State     int     `json:"state"`
	SSID      string  `json:"ssid"`
	BSSID     string  `json:"bssid"`
	RSSI      float64 `json:"rssi"`
	Quality   float64 `json:"quality"`
	RxMbps    float64 `json:"rx_mbps"`
	TxMbps    float64 `json:"tx_mbps"`
	Channel   int     `json:"channel"`
	Band      string  `json:"band"`
	Phy       string  `json:"phy"`
	Iface     string  `json:"iface"`
}

// bandOfMHz maps a channel center frequency to its WiFi band.
func bandOfMHz(f int) string {
	switch {
	case f >= 5925:
		return "6 GHz"
	case f >= 4900:
		return "5 GHz"
	case f >= 2400:
		return "2.4 GHz"
	}
	return ""
}

// channelOfMHz converts a center frequency to the IEEE 802.11 channel number.
func channelOfMHz(f int) int {
	switch {
	case f == 2484:
		return 14
	case f >= 2412 && f < 2484:
		return (f - 2407) / 5
	case f >= 5955:
		return (f - 5950) / 5
	case f >= 5000:
		return (f - 5000) / 5
	}
	return 0
}

// qualityOf approximates, from RSSI, the 0-100 signal quality Windows reports (inverse of its documented mapping).
func qualityOf(rssi float64) float64 { return max(0, min(100, 2*(rssi+100))) }
