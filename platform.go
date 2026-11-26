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

// BSS is one access point heard in a WiFi scan.
type BSS struct {
	BSSID     string  `json:"bssid"`
	SSID      string  `json:"ssid"`
	RSSI      float64 `json:"rssi"`
	Channel   int     `json:"channel"` // primary channel
	Band      string  `json:"band"`
	CenterMHz int     `json:"center_mhz"` // center of the whole block it occupies (not the primary channel when wider than 20 MHz)
	WidthMHz  int     `json:"width_mhz"`
}

// chanWidth reads the occupied block (center and width, MHz) from a beacon's information elements:
// HT operation (40 MHz), VHT operation (80/160 MHz on 5 GHz), HE operation 6 GHz info (6 GHz).
// shortcut: 80+80 is drawn as 80 MHz on the first segment and 320 MHz (WiFi 7 EHT) as 160; parse EHT operation if 320 MHz APs show up.
func chanWidth(ies []byte, primaryMHz int) (centerMHz, widthMHz int) {
	center, width := primaryMHz, 20
	ch5 := func(c int) int { return 5000 + 5*c }
	vht, he := 0, 0 // width found by VHT / HE elements, applied after HT whatever the element order
	vhtC, heC := 0, 0
	for len(ies) >= 2 && len(ies) >= 2+int(ies[1]) {
		id, d := ies[0], ies[2:2+int(ies[1])]
		ies = ies[2+len(d):]
		switch {
		case id == 61 && len(d) >= 2 && d[1]&0x04 != 0: // HT operation: secondary channel above (1) or below (3)
			switch d[1] & 0x03 {
			case 1:
				center, width = primaryMHz+10, 40
			case 3:
				center, width = primaryMHz-10, 40
			}
		case id == 192 && len(d) >= 3 && d[0] >= 1: // VHT operation: width 1 = 80/160/80+80 (2, 3 deprecated forms)
			s0, s1 := int(d[1]), int(d[2])
			vht, vhtC = 80, ch5(s0)
			if d[0] == 2 || d[0] == 1 && s1 != 0 && (s1-s0 == 8 || s0-s1 == 8) {
				vht = 160
				if d[0] == 1 {
					vhtC = ch5(s1)
				}
			}
		case id == 255 && len(d) >= 7 && d[0] == 36 && primaryMHz >= 5925: // HE operation, 6 GHz operation info
			p := int(d[1]) | int(d[2])<<8 | int(d[3])<<16
			off := 7
			if p&(1<<14) != 0 {
				off += 3 // VHT operation info
			}
			if p&(1<<15) != 0 {
				off++ // max co-hosted BSSID indicator
			}
			if p&(1<<17) == 0 || len(d) < off+5 {
				break
			}
			w, c0, c1 := d[off+1]&0x03, int(d[off+2]), int(d[off+3])
			he, heC = 20<<w, 5950+5*c0
			if w == 3 && c1 != 0 {
				heC = 5950 + 5*c1
			}
		}
	}
	switch {
	case he > 0:
		return heC, he
	case vht > 0:
		return vhtC, vht
	}
	return center, width
}
