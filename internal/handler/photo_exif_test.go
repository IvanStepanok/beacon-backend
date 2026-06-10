package handler

import (
	"bytes"
	"testing"
)

// TestStripJPEGMetadata locks the server-side EXIF/XMP/comment strip: the APP1
// (EXIF — GPS/device/timestamp) and COM segments are removed, while the image's
// rendering segments (APP0/JFIF, the SOS scan, EOI) are preserved byte-for-byte.
func TestStripJPEGMetadata(t *testing.T) {
	// SOI + APP1(EXIF "Exif\0\0") + APP0(JFIF, 2-byte payload) + COM("hi") + SOS + scan + EOI.
	app1 := []byte{0xFF, 0xE1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0x00, 0x00} // segLen 0x0008 = 2 + 6 payload
	app0 := []byte{0xFF, 0xE0, 0x00, 0x04, 'J', 'F'}                       // segLen 0x0004 = 2 + 2 payload
	com := []byte{0xFF, 0xFE, 0x00, 0x04, 'h', 'i'}                        // comment
	tail := []byte{0xFF, 0xDA, 0x00, 0x03, 0x01, 0x12, 0x34, 0xFF, 0xD9}   // SOS + scan + EOI
	in := append([]byte{0xFF, 0xD8}, app1...)
	in = append(in, app0...)
	in = append(in, com...)
	in = append(in, tail...)

	out := stripJPEGMetadata(in)

	if bytes.Contains(out, []byte("Exif")) {
		t.Errorf("APP1/EXIF segment was not stripped: % x", out)
	}
	if bytes.Contains(out, []byte("hi")) {
		t.Errorf("COM (comment) segment was not stripped: % x", out)
	}
	if !bytes.Contains(out, app0) {
		t.Errorf("APP0/JFIF (rendering) segment must be preserved: % x", out)
	}
	if !bytes.Contains(out, tail) {
		t.Errorf("SOS scan + EOI must be preserved verbatim: % x", out)
	}
	if len(out) < 2 || out[0] != 0xFF || out[1] != 0xD8 {
		t.Errorf("output must still start with the JPEG SOI marker: % x", out)
	}
	if out[len(out)-2] != 0xFF || out[len(out)-1] != 0xD9 {
		t.Errorf("output must still end with EOI: % x", out)
	}

	// Non-JPEG input is returned unchanged (PNG/WEBP pass-through).
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x01, 0x02}
	if got := stripJPEGMetadata(png); !bytes.Equal(got, png) {
		t.Errorf("non-JPEG input must be returned unchanged")
	}
}
