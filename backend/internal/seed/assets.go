package seed

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"

	"github.com/impactpay/backend/internal/models"
)

// asset is one demo evidence artefact to store.
//
// The seeder uploads through the same evidence service the API uses, so every
// artefact is written to content-addressed storage and analysed by the real
// deterministic extractor. Nothing about the verification result is hardcoded:
// the demo project's 87/100 HOLD is a consequence of what these files contain.
type asset struct {
	Kind           models.EvidenceKind
	RequirementKey string
	Filename       string
	ContentType    string
	Description    string
	Data           []byte
	Hints          models.EvidenceHints
}

// photoPNG builds a real 320x240 PNG and embeds a caption in a PNG tEXt chunk.
//
// Two things matter here. First, the bytes are a genuine image, so the pipeline's
// analyze_image tool really decodes dimensions and assesses quality. Second, the
// caption block is the kind of "OCR-able caption" the extractor looks for, so the
// asset identifier accompanying a photograph is read out of the file itself
// rather than taken on trust.
func photoPNG(caption string, variant int) []byte {
	const (
		width  = 320
		height = 240
	)
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	base := color.RGBA{
		R: uint8(24 + (variant*37)%180),
		G: uint8(60 + (variant*53)%160),
		B: uint8(90 + (variant*29)%140),
		A: 255,
	}
	for y := 0; y < height; y++ {
		shade := uint8((y * 24) / height)
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: clampByte(int(base.R) + int(shade)),
				G: clampByte(int(base.G) + int(shade)),
				B: clampByte(int(base.B) + int(shade)),
				A: 255,
			})
		}
	}

	// A deterministic noise band keeps every photograph byte-distinct and gives
	// the PNG encoder real entropy to work with, so the file is comfortably above
	// the extractor's minimum-usable-size floor.
	rnd := rand.New(rand.NewSource(int64(variant)*7919 + 104729))
	for y := 0; y < 48; y++ {
		for x := 0; x < width; x++ {
			v := uint8(rnd.Intn(256))
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}

	var buf bytes.Buffer
	// Encoding to a fixed-size RGBA image cannot fail for these dimensions.
	_ = png.Encode(&buf, img)
	return insertTextChunk(buf.Bytes(), "Description", caption)
}

func clampByte(v int) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	default:
		return uint8(v)
	}
}

// insertTextChunk splices a tEXt chunk in before IEND, producing a valid PNG.
func insertTextChunk(pngBytes []byte, keyword, text string) []byte {
	const iendLen = 12
	if len(pngBytes) < iendLen {
		return pngBytes
	}
	body := pngBytes[:len(pngBytes)-iendLen]
	iend := pngBytes[len(pngBytes)-iendLen:]

	data := append([]byte(keyword), 0x00)
	data = append(data, []byte(text)...)

	chunk := make([]byte, 0, len(data)+12)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	chunk = append(chunk, length[:]...)

	typeBytes := []byte("tEXt")
	chunk = append(chunk, typeBytes...)
	chunk = append(chunk, data...)

	crc := crc32.NewIEEE()
	_, _ = crc.Write(typeBytes)
	_, _ = crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	chunk = append(chunk, sum[:]...)

	out := make([]byte, 0, len(body)+len(chunk)+len(iend))
	out = append(out, body...)
	out = append(out, chunk...)
	out = append(out, iend...)
	return out
}

func textAsset(body string) []byte { return []byte(body) }

// ---------------------------------------------------------------------------
// Document builders
// ---------------------------------------------------------------------------

// invoiceText renders a supplier invoice containing a machine-readable total and
// quantity line. The extraction regexes read the total, so the milestone's budget
// reconciliation is computed from the document rather than asserted.
func invoiceText(invoiceNo, vendor, date string, totalMinor int64, quantity int, item string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", strings.ToUpper(vendor))
	b.WriteString("GSTIN 27AABCP1234K1Z8 — Pune, Maharashtra, India\n")
	b.WriteString("--------------------------------------------------------------------------\n")
	fmt.Fprintf(&b, "Invoice No: %s\n", invoiceNo)
	fmt.Fprintf(&b, "Vendor: %s\n", vendor)
	fmt.Fprintf(&b, "Date: %s\n", date)
	fmt.Fprintf(&b, "Description: %s\n", item)
	fmt.Fprintf(&b, "Qty: %d\n", quantity)
	fmt.Fprintf(&b, "Grand Total: INR %s\n", rupees(totalMinor))
	b.WriteString("--------------------------------------------------------------------------\n")
	b.WriteString("Payment due within 30 days. Supplied for the Clean Water Maharashtra programme.\n")
	return b.String()
}

func receiptText(receiptNo, vendor, date, reference string, totalMinor int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", strings.ToUpper(vendor))
	b.WriteString("--------------------------------------------------------------------------\n")
	fmt.Fprintf(&b, "Receipt No: %s\n", receiptNo)
	fmt.Fprintf(&b, "Vendor: %s\n", vendor)
	fmt.Fprintf(&b, "Date: %s\n", date)
	fmt.Fprintf(&b, "Against: %s\n", reference)
	fmt.Fprintf(&b, "Total: INR %s\n", rupees(totalMinor))
	b.WriteString("--------------------------------------------------------------------------\n")
	b.WriteString("Received with thanks as part payment against the equipment invoice.\n")
	return b.String()
}

// registryCSV renders a unit registry. The extractor scans it for asset
// identifiers, which is what lets the pipeline count serialised units at all.
func registryCSV(ids []string, village string) string {
	var b strings.Builder
	b.WriteString("unit_id,model,village,installed_on\n")
	for i, id := range ids {
		fmt.Fprintf(&b, "%s,AquaPure HF-100,%s,2026-04-%02d\n", id, village, (i%20)+2)
	}
	return b.String()
}

// siteSurveyReportText renders the survey report. Its site codes are the
// "villages surveyed" identifiers the pipeline counts for milestone 1.
func siteSurveyReportText(villages []siteVillage) string {
	var b strings.Builder
	b.WriteString("AQUAHOPE FOUNDATION\n")
	b.WriteString("Village site survey report — Clean Water Maharashtra\n")
	b.WriteString("Date: 2026-03-02\n")
	b.WriteString("--------------------------------------------------------------------------\n")

	names := make([]string, 0, len(villages))
	for _, v := range villages {
		names = append(names, v.Name)
	}
	fmt.Fprintf(&b, "Villages: %s\n", strings.Join(names, ", "))
	b.WriteString("--------------------------------------------------------------------------\n")
	b.WriteString("Site code, village, households, water source\n")
	for _, v := range villages {
		fmt.Fprintf(&b, "%s, %s, %d households, %s\n", v.Code, v.Name, v.Households, v.Source)
	}
	b.WriteString("--------------------------------------------------------------------------\n")
	b.WriteString("Site codes above identify each surveyed village and are used to match\n")
	b.WriteString("installation evidence to the correct location.\n")
	return b.String()
}

func completionReportText(households int, villages []string) string {
	var b strings.Builder
	b.WriteString("AQUAHOPE FOUNDATION\n")
	b.WriteString("Programme completion report — Clean Water Maharashtra\n")
	b.WriteString("Date: 2026-06-30\n")
	b.WriteString("--------------------------------------------------------------------------\n")
	fmt.Fprintf(&b, "Households: %d\n", households)
	fmt.Fprintf(&b, "Villages: %s\n", strings.Join(villages, ", "))
	b.WriteString("--------------------------------------------------------------------------\n")
	b.WriteString("Every installed unit was registered against a household during the\n")
	b.WriteString("completion survey. Beneficiary-level records are attached separately.\n")
	return b.String()
}

func beneficiaryRecordText(villages []string, households int) string {
	var b strings.Builder
	b.WriteString("Household beneficiary record — Clean Water Maharashtra\n")
	b.WriteString("--------------------------------------------------------------------------\n")
	fmt.Fprintf(&b, "Households: %d\n", households)
	fmt.Fprintf(&b, "Villages: %s\n", strings.Join(villages, ", "))
	b.WriteString("--------------------------------------------------------------------------\n")
	b.WriteString("household_id, village, persons, served_from\n")
	for i := 1; i <= households && i <= 600; i++ {
		village := villages[(i-1)%len(villages)]
		fmt.Fprintf(&b, "HH-%s-%04d, %s, %d, 2026-05-15\n", villageCode(village), i, village, 4+(i%3))
	}
	return b.String()
}

func villageCode(name string) string {
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))
	if len(code) > 4 {
		code = code[:4]
	}
	return code
}

// rupees renders minor units as a decimal amount, which is what the extraction
// total regex expects.
func rupees(minor int64) string {
	whole := minor / 100
	frac := minor % 100
	return fmt.Sprintf("%d.%02d", whole, frac)
}
