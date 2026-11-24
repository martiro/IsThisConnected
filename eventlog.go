package main

// Reads the WLAN AutoConfig event log to find out *why* the WiFi dropped,
// and shows Windows toast notifications. Both run only on events, never per cycle.

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

// lastDisconnectReason returns the reason of the most recent WLAN disconnect/failure
// event newer than maxAge. 8003 = disconnected, 8002 = connect failed, 11006 = security failure.
func lastDisconnectReason(maxAge time.Duration) (code int, reason string, found bool) {
	out, err := hiddenCmd("wevtutil", "qe", "Microsoft-Windows-WLAN-AutoConfig/Operational",
		"/q:*[System[(EventID=8003 or EventID=8002 or EventID=11006)]]",
		"/c:1", "/rd:true", "/f:xml").Output()
	if err != nil || len(out) == 0 {
		return 0, "", false
	}
	return parseWlanEvent(strings.ToValidUTF8(string(out), "?"), maxAge, time.Now())
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

func toast(title, body string) {
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	script := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;` +
		`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
		`$x = $t.GetElementsByTagName('text'); $x.Item(0).AppendChild($t.CreateTextNode('` + esc(title) + `')) > $null; $x.Item(1).AppendChild($t.CreateTextNode('` + esc(body) + `')) > $null;` +
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($t))`
	hiddenCmd("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Start()
}
