package main

import (
	"os"
	"testing"
)

func TestMigrateOldData(t *testing.T) {
	base := t.TempDir()
	old, d := base+"/WiFiMon", base+"/IsThisConnected"
	os.MkdirAll(old, 0o755)
	os.WriteFile(old+"/wifimon.db", []byte("history"), 0o644)
	os.WriteFile(old+"/config.json", []byte("{}"), 0o644)
	migrateOldData(old, d)
	if b, err := os.ReadFile(d + "/isthisconnected.db"); err != nil || string(b) != "history" {
		t.Fatalf("database not migrated: %v", err)
	}
	if _, err := os.Stat(d + "/config.json"); err != nil {
		t.Error("config not migrated")
	}
	os.MkdirAll(old, 0o755) // a second run must never overwrite data already in the new folder
	os.WriteFile(old+"/wifimon.db", []byte("stale"), 0o644)
	migrateOldData(old, d)
	if b, _ := os.ReadFile(d + "/isthisconnected.db"); string(b) != "history" {
		t.Error("existing data overwritten")
	}
}
