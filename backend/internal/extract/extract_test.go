package extract_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/impactpay/backend/internal/extract"
	"github.com/impactpay/backend/internal/models"
)

// TestScanUnitIDsIsPrecise pins the serial-number shape. Precision matters more
// than recall here: a false positive inflates a verified quantity and could
// release funding for work that was never evidenced.
func TestScanUnitIDsIsPrecise(t *testing.T) {
	text := "Installed units CW-MH-0001 and SUR-KAN-001. Model HF-100. Order AQINV20260142."
	got := extract.ScanUnitIDs(text)

	want := map[string]bool{"CW-MH-0001": true, "SUR-KAN-001": true}
	if len(got) != len(want) {
		t.Fatalf("ScanUnitIDs = %v, want exactly %d identifiers", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected identifier %q", id)
		}
	}
}

// TestCaptionIsReadFromPngMetadataAndNoiseIsIgnored proves that an image's
// caption is read from structured metadata while compression noise is not
// mistaken for serial numbers.
func TestCaptionIsReadFromPngMetadataAndNoiseIsIgnored(t *testing.T) {
	data := pngWithCaption(t, "installed filtration unit CW-MH-0042 at Kandhwad village")

	facts := extract.Artefact(models.EvidencePhotograph, "installation.png", "image/png", data,
		models.EvidenceHints{})
	if facts.Quality != "ok" {
		t.Fatalf("quality = %q, want ok (width %d, height %d)", facts.Quality, facts.Width, facts.Height)
	}
	if len(facts.UnitIDs) != 1 || facts.UnitIDs[0] != "CW-MH-0042" {
		t.Fatalf("unit ids = %v, want [CW-MH-0042]", facts.UnitIDs)
	}
}

func TestDuplicateCaptionReadsTheSameIdentifierOnce(t *testing.T) {
	caption := "unit CW-MH-0099 unit CW-MH-0099 unit CW-MH-0099"
	data := pngWithCaption(t, caption)
	facts := extract.Artefact(models.EvidencePhotograph, "a.png", "image/png", data, models.EvidenceHints{})
	if len(facts.UnitIDs) != 1 {
		t.Fatalf("unit ids = %v, want a single de-duplicated identifier", facts.UnitIDs)
	}
}

func TestInvoiceParsing(t *testing.T) {
	body := "PUREFLOW SYSTEMS PVT LTD\n" +
		"Invoice No: AQINV20260142\n" +
		"Vendor: PureFlow Systems Pvt Ltd\n" +
		"Date: 2026-03-14\n" +
		"Qty: 100\n" +
		"Grand Total: INR 145000.00\n"

	facts := extract.Artefact(models.EvidenceInvoice, "invoice.txt", "text/plain", []byte(body), models.EvidenceHints{})
	if facts.AmountMinor != 14_500_000 {
		t.Errorf("amount = %d, want 14500000", facts.AmountMinor)
	}
	if facts.StatedQuantity != 100 {
		t.Errorf("stated quantity = %d, want 100", facts.StatedQuantity)
	}
	if facts.InvoiceNumber != "AQINV20260142" {
		t.Errorf("invoice number = %q", facts.InvoiceNumber)
	}
	if facts.Vendor != "PureFlow Systems Pvt Ltd" {
		t.Errorf("vendor = %q", facts.Vendor)
	}
	if facts.DocumentDate != "2026-03-14" {
		t.Errorf("date = %q", facts.DocumentDate)
	}
}

func TestRegistryCountsEverySerial(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("unit_id,model,village,installed_on\n")
	for i := 1; i <= 12; i++ {
		b.WriteString(registryRow(i))
	}
	facts := extract.Artefact(models.EvidenceUnitRegistry, "registry.csv", "text/csv", b.Bytes(), models.EvidenceHints{})
	if len(facts.UnitIDs) != 12 {
		t.Fatalf("registry yielded %d identifiers, want 12: %v", len(facts.UnitIDs), facts.UnitIDs)
	}
}

// ---------------------------------------------------------------------------

func registryRow(n int) string {
	id := "CW-MH-"
	switch {
	case n < 10:
		id += "000"
	case n < 100:
		id += "00"
	default:
		id += "0"
	}
	return id + itoa(n) + ",AquaPure HF-100,Kandhwad,2026-04-02\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// pngWithCaption builds a small valid PNG carrying a tEXt chunk with the caption.
func pngWithCaption(t *testing.T, caption string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 220, 210))
	for y := 0; y < 210; y++ {
		for x := 0; x < 220; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8((x * 3) % 255), G: uint8((y * 5) % 255), B: 90, A: 255})
		}
	}
	// A noise band keeps the file above the extractor's minimum-usable-size floor
	// without needing a real photograph fixture.
	state := uint32(12345)
	for y := 0; y < 60; y++ {
		for x := 0; x < 220; x++ {
			state = state*1664525 + 1013904223
			v := uint8(state >> 24)
			img.SetRGBA(x, y, color.RGBA{R: v, G: v ^ 0x5a, B: v ^ 0xa5, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	encoded := buf.Bytes()

	payload := append([]byte("Description"), 0x00)
	payload = append(payload, []byte(caption)...)

	var chunk bytes.Buffer
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	chunk.Write(length[:])
	chunk.WriteString("tEXt")
	chunk.Write(payload)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte("tEXt"))
	_, _ = crc.Write(payload)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	chunk.Write(sum[:])

	const iendLen = 12
	body := encoded[:len(encoded)-iendLen]
	iend := encoded[len(encoded)-iendLen:]

	out := make([]byte, 0, len(encoded)+chunk.Len())
	out = append(out, body...)
	out = append(out, chunk.Bytes()...)
	out = append(out, iend...)
	return out
}
