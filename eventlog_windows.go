package main

// Windows: disconnect reason from the WLAN AutoConfig event log, toast notifications.
// Both run only on events, never per cycle.

import (
	"strings"
	"time"
)

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

func toast(title, body string) {
	esc := func(s string) string { return strings.ReplaceAll(s, "'", "''") }
	script := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;` +
		`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
		`$x = $t.GetElementsByTagName('text'); $x.Item(0).AppendChild($t.CreateTextNode('` + esc(title) + `')) > $null; $x.Item(1).AppendChild($t.CreateTextNode('` + esc(body) + `')) > $null;` +
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show([Windows.UI.Notifications.ToastNotification]::new($t))`
	hiddenCmd("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Start()
}
