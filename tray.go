package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"time"

	"fyne.io/systray"
)

var trayColors = map[string]color.RGBA{
	"ok":     {0x22, 0xa0, 0x55, 0xff},
	"slow":   {0xe8, 0xa3, 0x17, 0xff},
	"down":   {0xd9, 0x3b, 0x3b, 0xff},
	"paused": {0x8a, 0x8f, 0x98, 0xff},
}

// dotIcon renders a 32x32 WiFi-like icon (filled circle with arcs cut out) and, on Windows, wraps the PNG in an ICO container.
func dotIcon(c color.RGBA) []byte {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			d := dx*dx + dy*dy
			if d <= 15*15 {
				img.Set(x, y, c)
				if d >= 6.5*6.5 && d <= 9*9 && dy < -0.5 && dy < -abs(dx)*0.6 { // white arc for contrast
					img.Set(x, y, color.White)
				}
				if d <= 3*3 && dy < 3 {
					img.Set(x, y, color.White)
				}
			}
		}
	}
	var p bytes.Buffer
	png.Encode(&p, img)
	if runtime.GOOS != "windows" { // the tray takes PNG on Linux and macOS, ICO on Windows
		return p.Bytes()
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1})                 // ICONDIR: reserved, type=icon, count
	ico.Write([]byte{n, n, 0, 0})                                              // width, height, colors, reserved
	binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})                   // planes, bpp
	binary.Write(&ico, binary.LittleEndian, []uint32{uint32(p.Len()), 6 + 16}) // size, offset
	ico.Write(p.Bytes())
	return ico.Bytes()
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// The tray must run on the main OS thread (required on macOS, harmless elsewhere).
func init() { runtime.LockOSThread() }

func runTray(a *App) {
	icons := map[string][]byte{}
	for k, c := range trayColors {
		icons[k] = dotIcon(c)
	}
	systray.Run(func() {
		systray.SetIcon(icons["ok"])
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
			if s != last {
				systray.SetIcon(icons[s])
				last = s
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
