// Package qr renders a VPN client config as a QR code a phone scans.
//
// It used to pipe the config to `qrencode`, a binary every box had to have
// installed (and without it the admin UI showed a placeholder telling the
// operator to apt-get it). It is rsc.io/qr now, in process, so hz needs no
// qrencode (2026-10-30). The data never reaches argv either way: a client
// config holds its private key, and argv is readable by every process.
package qr

import (
	"fmt"
	"strings"

	rscqr "rsc.io/qr"
)

// margin is the quiet zone in modules, as `qrencode -m 2` drew it. rsc.io/qr
// returns the bare grid.
const margin = 2

// moduleSize is the SVG pixels per module, as `qrencode -s 8` drew it.
const moduleSize = 8

func encode(data string) (*rscqr.Code, error) {
	return rscqr.Encode(data, rscqr.L) // qrencode's default level
}

// dark reports whether the module at (x, y) of the code WITH its quiet zone
// is dark.
func dark(c *rscqr.Code, x, y int) bool {
	x, y = x-margin, y-margin
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.Black(x, y)
}

// GenerateSVG renders data as an SVG QR code. A config too long for any QR
// version gets a placeholder that says so rather than an image that would not
// scan. size is kept for the callers' signature; the image scales by viewBox.
func GenerateSVG(data string, size int) string {
	c, err := encode(data)
	if err != nil {
		return unavailableSVG(size, err)
	}
	n := c.Size + 2*margin
	px := n * moduleSize

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`, n, n, px, px)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/><path fill="#000000" d="`, n, n)
	for y := range n {
		for x := range n {
			if dark(c, x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// Terminal renders data as a QR code for a UTF-8 terminal, two module rows
// per text row with half blocks. Light modules are drawn white on black,
// explicitly, so the code reads the right way round on a dark or a light
// terminal — what `qrencode -t ANSIUTF8` did.
func Terminal(data string) (string, error) {
	c, err := encode(data)
	if err != nil {
		return "", fmt.Errorf("no QR code (%v); the config above is the same thing", err)
	}
	n := c.Size + 2*margin
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.WriteString("\x1b[37;40m")
		for x := range n {
			top, bottom := !dark(c, x, y), y+1 < n && !dark(c, x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String(), nil
}

// unavailableSVG is the placeholder for data no QR code can hold.
func unavailableSVG(size int, err error) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 200" width="%d" height="%d">
  <rect width="200" height="200" fill="#f0f0f0" stroke="#ccc"/>
  <text x="100" y="90" text-anchor="middle" font-family="sans-serif" font-size="14" fill="#666">QR code unavailable</text>
  <text x="100" y="120" text-anchor="middle" font-family="sans-serif" font-size="11" fill="#666">%s</text>
</svg>`, size, size, svgEscape(err.Error()))
}

func svgEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
