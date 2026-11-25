package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// appDataBase is where the data folder lives: %LOCALAPPDATA%.
func appDataBase() string { return os.Getenv("LOCALAPPDATA") }

// Autostart via HKCU Run key: per-user, needs no admin (a logon scheduled task would).
func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	k.DeleteValue("WiFiMon") // entry of the former name, if any
	return k.SetStringValue(appName, `"`+exe+`"`)
}

func uninstallAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	k.DeleteValue("WiFiMon") // former name
	return k.DeleteValue(appName)
}
