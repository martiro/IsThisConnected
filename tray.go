package main

import (
	"fmt"
	"runtime"
	"time"

	"fyne.io/systray"
)

// The tray must run on the main OS thread (required on macOS, harmless elsewhere).
func init() { runtime.LockOSThread() }

func runTray(a *App) {
	systray.Run(func() {
		systray.SetIcon(trayIcon("ok", lightBar()))
		systray.SetTitle(appName)
		systray.SetTooltip(appName)
		mOpen := systray.AddMenuItem("Open dashboard", "")
		mTest := systray.AddMenuItem("Run speed test now", "")
		mPause := systray.AddMenuItem("Pause monitoring", "")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit", "")
		systray.SetOnTapped(openBrowser)

		last := ""
		refresh := func() {
			s := a.status()
			if key := fmt.Sprint(s, lightBar()); key != last { // status changed, or the user switched light/dark theme
				systray.SetIcon(trayIcon(s, lightBar()))
				last = key
			}
			if a.paused.Load() {
				mPause.SetTitle("Resume monitoring")
			} else {
				mPause.SetTitle("Pause monitoring")
			}
			systray.SetTooltip(a.tooltip(s))
		}
		a.onChange = refresh
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				refresh()
				select {
				case <-t.C:
				case <-mOpen.ClickedCh:
					openBrowser()
				case <-mTest.ClickedCh:
					go func() {
						mbps, err := a.speedtest(true)
						if err != nil {
							toast("Speed test", err.Error())
						} else {
							toast("Speed test", fmt.Sprintf("Download: %.1f Mbit/s", mbps))
						}
					}()
				case <-mPause.ClickedCh:
					a.togglePause()
				case <-mQuit.ClickedCh:
					systray.Quit()
					return
				}
			}
		}()
	}, func() {})
}

func (a *App) tooltip(status string) string {
	label := map[string]string{"ok": "OK", "slow": "Slowdown", "down": "Disconnected", "paused": "Paused"}[status]
	a.st.mu.Lock()
	defer a.st.mu.Unlock()
	t := appName + " – " + label
	if a.st.Wifi.Connected {
		t += "\n" + a.st.Wifi.SSID + fmt.Sprintf(" (%.0f dBm)", a.st.Wifi.RSSI)
	}
	if v, ok := a.st.fresh("rtt/"+a.cfg.InternetTargets[0], now(), 3000); ok {
		t += fmt.Sprintf("\nPing %s: %.0f ms", a.cfg.InternetTargets[0], v)
	} else {
		t += "\nPing " + a.cfg.InternetTargets[0] + ": --"
	}
	t += fmt.Sprintf("\nTraffic ↓ %.1f Mbit/s", a.st.vals["thr/rx"])
	if a.st.Metered {
		t += "\nMetered connection"
	}
	if r := []rune(t); len(r) > 127 { // NOTIFYICONDATA tooltip limit (UTF-16 units; our runes are all BMP)
		t = string(r[:127])
	}
	return t
}
