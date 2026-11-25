//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var instanceLock *os.File // kept open (and locked) for the life of the process

// singleInstance takes an exclusive lock on a per-user file; the OS releases it when the process exits.
func singleInstance() bool {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), fmt.Sprintf("isthisconnected-%d.lock", os.Getuid())), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return true // cannot tell: better run twice than not at all
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return false
	}
	instanceLock = f
	return true
}

// msgBox: on Linux and macOS the subcommands (install, uninstall) are run from a terminal.
func msgBox(text string) { fmt.Println(text) }

func openBrowser() { exec.Command(browserCmd, "http://"+listenAddr).Start() }
