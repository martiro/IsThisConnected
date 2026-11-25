package main

import (
	"bytes"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderLogo(t *testing.T) {
	red := qColors["down"]
	img := renderLogo(32, inkDark, red, 1)
	var ink, q, clear int
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			switch c := img.NRGBAAt(x, y); {
			case c.A == 0:
				clear++
			case c.A == 255 && c == red:
				q++
			case c.A == 255 && c == inkDark:
				ink++
			}
		}
	}
	// waves, question mark and transparent background must all be there
	if ink < 20 || q < 20 || clear < 400 {
		t.Errorf("unexpected 32px logo: ink %d, question mark %d, transparent %d pixels", ink, q, clear)
	}
	if dot := img.NRGBAAt(16, 29); dot.A == 0 { // the dot sits low in the center
		t.Errorf("no dot at the bottom center: %v", dot)
	}

	// Windows tray icon: an ICO with five PNG images; elsewhere a PNG
	b := trayIcon("slow", true)
	if len(b) < 6 || (b[2] != 1 || b[4] != 5) && !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Errorf("tray icon is neither a 5-image ICO nor a PNG: % x", b[:8])
	}

	// Set LOGO_PREVIEW=<dir> to write the tray icons as PNG files and look at them.
	if dir := os.Getenv("LOGO_PREVIEW"); dir != "" {
		for _, st := range []string{"ok", "slow", "down", "paused"} {
			for light, ink := range map[bool]color.NRGBA{true: inkDark, false: inkLight} {
				q, ok := qColors[st]
				if !ok {
					q = ink
				}
				for _, n := range []int{16, 24, 32, 48} {
					f, _ := os.Create(filepath.Join(dir, st+map[bool]string{true: "-light-", false: "-dark-"}[light]+itoa(n)+".png"))
					png.Encode(f, renderLogo(n, ink, q, 100.0/88))
					f.Close()
				}
			}
		}
	}
}

func itoa(n int) string { return string(rune('0'+n/10)) + string(rune('0'+n%10)) }
