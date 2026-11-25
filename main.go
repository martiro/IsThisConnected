// IsThisConnected: lightweight background monitor of WiFi/Internet quality (Windows; Linux and macOS experimental).
// (Say it out loud: "is disconnected".)
package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
)

const appName = "IsThisConnected"

func dataDir() string {
	base := appDataBase()
	d := filepath.Join(base, appName)
	migrateOldData(filepath.Join(base, "WiFiMon"), d)
	os.MkdirAll(filepath.Join(d, "logs"), 0o755)
	return d
}

// migrateOldData moves, once, the history kept under the project's former name (WiFiMon).
// If the move fails (e.g. the old version is still running) the old folder is left untouched.
func migrateOldData(old, d string) {
	if _, err := os.Stat(d); err == nil {
		return
	}
	if _, err := os.Stat(old); err != nil {
		return
	}
	if err := os.Rename(old, d); err != nil {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Rename(filepath.Join(d, "wifimon.db"+suffix), filepath.Join(d, "isthisconnected.db"+suffix))
	}
}

// setupLog: shortcut: rotates only at startup (keeps one .1 file), enough for a log that grows by events only.
func setupLog(dir string) {
	p := filepath.Join(dir, "logs", "isthisconnected.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 5<<20 {
		os.Rename(p, p+".1")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}
}

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "install":
		if err := installAutostart(); err != nil {
			msgBox("Install failed: " + err.Error())
		} else {
			msgBox("IsThisConnected will start automatically when you sign in.")
		}
		return
	case "uninstall":
		if err := uninstallAutostart(); err != nil {
			msgBox("Uninstall failed: " + err.Error())
		} else {
			msgBox("Automatic start removed.")
		}
		return
	case "open":
		openBrowser()
		return
	case "", "run":
	default:
		msgBox("Usage: isthisconnected [install | uninstall | open]")
		return
	}
	if !singleInstance() {
		openBrowser()
		return
	}
	debug.SetGCPercent(50)         // always-on background tool: trade a little CPU for a smaller heap
	debug.SetMemoryLimit(20 << 20) // soft limit, GC works harder near it
	dir := dataDir()
	setupLog(dir)
	cfg := loadConfig(dir)
	store, err := openStore(filepath.Join(dir, "isthisconnected.db"))
	if err != nil {
		log.Printf("database: %v", err)
		msgBox("Cannot open the database: " + err.Error())
		return
	}
	log.Printf("started, data in %s", dir)
	app := newApp(cfg, store)
	app.start()
	go app.serve()
	runTray(app) // blocks until "Quit"
	app.stop()
	log.Printf("stopped")
}
