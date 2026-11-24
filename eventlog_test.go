package main

import (
	"testing"
	"time"
)

func TestParseWlanEvent(t *testing.T) {
	x := `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><EventID>8003</EventID><TimeCreated SystemTime='2026-11-24T07:52:22.6905491Z'/></System><EventData><Data Name='SSID'>Casa</Data><Data Name='Reason'>The network is disconnected by the driver.</Data><Data Name='ReasonCode'>0</Data></EventData></Event>`
	at := time.Date(2026, 11, 24, 7, 53, 0, 0, time.UTC)
	code, reason, ok := parseWlanEvent(x, 2*time.Minute, at)
	if !ok || code != 0 || reason != "The network is disconnected by the driver." {
		t.Errorf("got %d %q %v", code, reason, ok)
	}
	if _, _, ok := parseWlanEvent(x, 2*time.Minute, at.Add(time.Hour)); ok {
		t.Error("stale event accepted")
	}
}
