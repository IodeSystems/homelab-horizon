package qr

import (
	"strings"
	"testing"
)

const sampleConfig = "[Interface]\nPrivateKey = aGVsbG8gd29ybGQgdGhpcyBpcyBub3QgYSByZWFsIGtleSE=\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = cHVibGljIGtleSBleGFtcGxlIG9ubHkgbm90IHJlYWwhIQ==\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0\n"

// The SVG draws exactly the code's dark modules, inside a light quiet zone of
// `margin` modules — what qrencode drew. (Decoding was checked once with a
// QR reader on 2026-10-30; this pins that the drawing is the code.)
func TestSVGDrawsTheCodeInsideAQuietZone(t *testing.T) {
	c, err := encode(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	svg := GenerateSVG(sampleConfig, 256)
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") || !strings.Contains(svg, "xmlns") {
		t.Fatalf("not an SVG document: %.80s…", svg)
	}

	want := 0
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				want++
			}
		}
	}
	if got := strings.Count(svg, "h1v1h-1z"); got != want {
		t.Errorf("SVG draws %d modules, the code has %d dark", got, want)
	}
	// The finder pattern's corner is the first dark module, offset by the margin.
	if !strings.Contains(svg, `d="M2 2h1v1h-1z`) {
		t.Errorf("first dark module is not at (margin, margin): %.200s", svg[strings.Index(svg, `d="`):])
	}
	for _, x := range []int{0, 1} {
		if dark(c, x, x) {
			t.Errorf("quiet zone module (%d,%d) is dark", x, x)
		}
	}
}

// Two module rows per text row, the whole code plus its quiet zone, each row
// reset so the colours never bleed into what the terminal prints next.
func TestTerminalCoversTheCodeAndResetsEachRow(t *testing.T) {
	c, err := encode(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Terminal(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	n := c.Size + 2*margin
	rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(rows) != (n+1)/2 {
		t.Errorf("rows = %d, want %d for a %d-module code", len(rows), (n+1)/2, n)
	}
	for i, r := range rows {
		if !strings.HasSuffix(r, "\x1b[0m") {
			t.Fatalf("row %d does not reset its colours", i)
		}
		if cells := len([]rune(strings.TrimSuffix(strings.TrimPrefix(r, "\x1b[37;40m"), "\x1b[0m"))); cells != n {
			t.Fatalf("row %d has %d cells, want %d", i, cells, n)
		}
	}
}

// More data than any QR version holds is a placeholder that says why, not an
// image that will not scan, and the terminal form is an error.
func TestTooMuchDataSaysSo(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	if svg := GenerateSVG(huge, 200); !strings.Contains(svg, "QR code unavailable") {
		t.Errorf("want the placeholder, got %.80s…", svg)
	}
	if _, err := Terminal(huge); err == nil {
		t.Error("Terminal of 5000 bytes: want an error")
	}
}
