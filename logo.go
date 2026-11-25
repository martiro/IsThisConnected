package main

// The logo (WiFi waves + a question mark sharing the dot), drawn at any pixel size for the tray.
// assets/logo.json holds the geometry in a 0..100 box; docs/logo.svg and the dashboard use the same shapes.

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
	"sync"
)

//go:embed assets/logo.json
var logoJSON []byte

type logoGeometry struct {
	W       float64    `json:"w"`   // stroke width
	Dot     [3]float64 `json:"dot"` // cx, cy, r
	Strokes []struct {
		Role string       `json:"role"` // "w" = WiFi wave (ink), "q" = question mark
		Pts  [][2]float64 `json:"pts"`
	} `json:"strokes"`
}

var logo = sync.OnceValue(func() logoGeometry {
	var g logoGeometry
	if err := json.Unmarshal(logoJSON, &g); err != nil {
		panic("assets/logo.json: " + err.Error()) // embedded at build time: can only fail in development
	}
	return g
})

var (
	inkDark  = color.NRGBA{0x1b, 0x1f, 0x24, 0xff} // waves and dot on a light background
	inkLight = color.NRGBA{0xff, 0xff, 0xff, 0xff} // waves and dot on a dark background
	qColors  = map[string]color.NRGBA{             // the question mark tells the status; "ok" uses the ink
		"slow":   {0xe8, 0xa3, 0x17, 0xff},
		"down":   {0xe5, 0x25, 0x2a, 0xff},
		"paused": {0x8a, 0x8f, 0x98, 0xff},
	}
)

// renderLogo draws the logo n x n pixels: waves and dot in ink, the question mark in q.
// zoom > 1 enlarges it around the center (the geometry keeps an 8% margin). Anti-aliasing comes from the
// exact distance of each pixel center to the strokes: half a pixel of soft edge, no supersampling needed.
func renderLogo(n int, ink, q color.NRGBA, zoom float64) *image.NRGBA {
	g := logo()
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	s := float64(n) / 100 * zoom // pixels per logo unit
	cover := func(dist float64) float64 { return max(0, min(1, dist*s+0.5)) }
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			x := (float64(px)+0.5-float64(n)/2)/s + 50
			y := (float64(py)+0.5-float64(n)/2)/s + 50
			var aInk, aQ float64
			for _, st := range g.Strokes {
				c := cover(g.W/2 - polylineDist(st.Pts, x, y))
				if st.Role == "q" {
					aQ = max(aQ, c)
				} else {
					aInk = max(aInk, c)
				}
			}
			aInk = max(aInk, cover(g.Dot[2]-math.Hypot(x-g.Dot[0], y-g.Dot[1])))
			a := aQ + aInk*(1-aQ) // question mark drawn over the waves
			if a == 0 {
				continue
			}
			mix := func(cq, ci uint8) uint8 { return uint8((float64(cq)*aQ + float64(ci)*aInk*(1-aQ)) / a) }
			img.SetNRGBA(px, py, color.NRGBA{mix(q.R, ink.R), mix(q.G, ink.G), mix(q.B, ink.B), uint8(a*255 + 0.5)})
		}
	}
	return img
}

// polylineDist is the distance from (x, y) to the polyline.
func polylineDist(pts [][2]float64, x, y float64) float64 {
	best := math.Inf(1)
	for i := 1; i < len(pts); i++ {
		ax, ay, bx, by := pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1]
		dx, dy := bx-ax, by-ay
		t := 0.0
		if l := dx*dx + dy*dy; l > 0 {
			t = max(0, min(1, ((x-ax)*dx+(y-ay)*dy)/l))
		}
		best = min(best, math.Hypot(x-ax-t*dx, y-ay-t*dy))
	}
	return best
}

var (
	iconMu    sync.Mutex
	iconCache = map[[2]string][]byte{}
)

// trayIcon returns the tray icon for a status: ICO with several sizes on Windows, PNG elsewhere.
// lightBar = the taskbar or menu bar is light, so the waves are drawn dark.
func trayIcon(status string, lightBar bool) []byte {
	key := [2]string{status, map[bool]string{true: "light", false: "dark"}[lightBar]}
	iconMu.Lock()
	defer iconMu.Unlock()
	if b, ok := iconCache[key]; ok {
		return b
	}
	ink := inkLight
	if lightBar {
		ink = inkDark
	}
	q, ok := qColors[status]
	if !ok {
		q = ink
	}
	const zoom = 100.0 / 88 // tray icons fill their square: drop most of the margin
	var b []byte
	if runtime.GOOS == "windows" {
		var imgs []*image.NRGBA
		for _, n := range []int{16, 20, 24, 32, 48} { // Windows picks the size matching the display scaling
			imgs = append(imgs, renderLogo(n, ink, q, zoom))
		}
		b = icoFile(imgs)
	} else {
		var p bytes.Buffer
		png.Encode(&p, renderLogo(64, ink, q, zoom))
		b = p.Bytes()
	}
	iconCache[key] = b
	return b
}

// icoFile packs PNG-compressed images into an ICO container (supported since Windows Vista).
func icoFile(imgs []*image.NRGBA) []byte {
	var pngs [][]byte
	for _, img := range imgs {
		var p bytes.Buffer
		png.Encode(&p, img)
		pngs = append(pngs, p.Bytes())
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, uint16(len(imgs))}) // ICONDIR: reserved, type=icon, count
	offset := 6 + 16*len(imgs)
	for i, img := range imgs {
		n := img.Bounds().Dx()
		ico.Write([]byte{byte(n), byte(n), 0, 0})                                               // width, height (256 = 0), colors, reserved
		binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})                                // planes, bits per pixel
		binary.Write(&ico, binary.LittleEndian, []uint32{uint32(len(pngs[i])), uint32(offset)}) // size, offset
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		ico.Write(p)
	}
	return ico.Bytes()
}
