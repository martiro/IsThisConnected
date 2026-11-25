package main

// Parser for the Windows WLAN AutoConfig events that tell *why* the WiFi dropped
// (pure Go, so it is tested on every platform; the query itself is in eventlog_windows.go).

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

type wlanEvent struct {
	System struct {
		EventID     int `xml:"EventID"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	Data []struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:",chardata"`
	} `xml:"EventData>Data"`
}

func parseWlanEvent(s string, maxAge time.Duration, now time.Time) (code int, reason string, found bool) {
	var ev wlanEvent
	if xml.Unmarshal([]byte(s), &ev) != nil {
		return 0, "", false
	}
	if t, err := time.Parse(time.RFC3339Nano, ev.System.TimeCreated.SystemTime); err != nil || now.Sub(t) > maxAge {
		return 0, "", false
	}
	for _, d := range ev.Data {
		v := strings.TrimSpace(d.Value)
		switch {
		case strings.HasSuffix(d.Name, "ReasonCode"):
			code, _ = strconv.Atoi(v)
		case strings.HasSuffix(d.Name, "Reason"):
			reason = v
		}
	}
	return code, reason, true
}
