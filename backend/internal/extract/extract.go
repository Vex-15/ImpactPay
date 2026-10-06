// Package extract turns a stored evidence artefact into structured facts.
//
// This is the "eyes" of the verification pipeline: it is deliberately
// deterministic and side-effect free. Given the same bytes and the same uploader
// claims it always returns the same facts, which is what makes the demo
// reproducible and every verification decision replayable.
//
// The pipeline never trusts an uploader claim: declared unit IDs, amounts and
// quantities are merged into the extracted facts but are separately tracked so
// the comparison stage can distinguish "read off the invoice" from "asserted by
// the organization".
package extract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/impactpay/backend/internal/core"
	"github.com/impactpay/backend/internal/models"
)

// MaxTextScan bounds how much of a binary file is scanned for ASCII text.
const MaxTextScan = 512 * 1024

// Result is the extraction outcome for one artefact.
type Result struct {
	Facts models.ExtractedFacts `json:"facts"`
}

// Artefact extracts facts from raw bytes plus the uploader's declared hints.
func Artefact(kind models.EvidenceKind, filename, contentType string, data []byte, hints models.EvidenceHints) models.ExtractedFacts {
	facts := models.ExtractedFacts{
		Kind:           string(kind),
		ExtractionMode: "deterministic",
	}

	switch kind {
	case models.EvidencePhotograph, models.EvidenceLocation:
		extractImage(&facts, filename, contentType, data)
	case models.EvidenceInvoice, models.EvidenceReceipt:
		extractFinancialDocument(&facts, filename, contentType, data)
	case models.EvidenceUnitRegistry:
		extractRegistry(&facts, filename, contentType, data)
	case models.EvidenceBeneficiaryRecord:
		extractBeneficiaryRecord(&facts, filename, contentType, data)
	default:
		extractDocument(&facts, filename, contentType, data)
	}

	mergeHints(&facts, hints)
	finalise(&facts)
	return facts
}

// ---------------------------------------------------------------------------
// Images
// ---------------------------------------------------------------------------

func extractImage(facts *models.ExtractedFacts, filename, contentType string, data []byte) {
	facts.DocumentType = "image"
	w, h, format, err := imageDimensions(data)
	if err != nil {
		facts.Quality = "insufficient"
		facts.Notes = append(facts.Notes, "Image header could not be decoded: "+err.Error())
		return
	}
	facts.Width = w
	facts.Height = h
	facts.Notes = append(facts.Notes, fmt.Sprintf("Decoded %s image %dx%d", format, w, h))

	// A photographic artefact is only usable as installation proof when the
	// organisation records which asset it shows. Without that identifier the
	// photo cannot be counted toward a quantity requirement.
	switch {
	case len(data) < 1024:
		facts.Quality = "insufficient"
		facts.Notes = append(facts.Notes, "File is too small to be a usable field photograph")
	case w < 200 || h < 200:
		facts.Quality = "low"
		facts.Notes = append(facts.Notes, "Resolution below the 200px evidence floor")
	default:
		facts.Quality = "ok"
	}

	// Photographs frequently carry a caption written by the field application.
	// Only *structured* metadata is read: scraping the raw image bytes would
	// happily turn compression noise into imaginary asset serial numbers, and a
	// verification agent that counts noise is worse than one that counts nothing.
	if caption := imageCaption(data); caption != "" {
		facts.Notes = append(facts.Notes, "Read caption metadata from the image")
		if ids := ScanUnitIDs(caption); len(ids) > 0 {
			facts.UnitIDs = append(facts.UnitIDs, ids...)
			facts.Notes = append(facts.Notes, fmt.Sprintf("Read %d identifier token(s) from the caption", len(ids)))
		}
	}
}

// imageCaption returns the text of a PNG's tEXt / iTXt metadata chunks.
//
// PNG is the format ImpactPay's own field application writes, and its metadata
// chunks are self-describing, so the caption can be read exactly rather than
// guessed at. Other image containers are supported for storage and quality
// assessment but carry no readable caption; their asset identifiers must be
// supplied by the uploader.
func imageCaption(data []byte) string {
	if len(data) < 8 || string(data[1:4]) != "PNG" {
		return ""
	}
	var b strings.Builder
	i := 8 // skip the 8-byte signature
	for i+8 <= len(data) {
		length := int(be32(data[i : i+4]))
		chunkType := string(data[i+4 : i+8])
		start := i + 8
		end := start + length
		if length < 0 || end+4 > len(data) {
			break
		}
		payload := data[start:end]

		switch chunkType {
		case "tEXt":
			if k := bytes.IndexByte(payload, 0); k >= 0 && k+1 <= len(payload) {
				b.Write(payload[k+1:])
				b.WriteByte('\n')
			}
		case "iTXt":
			// keyword \0 compressionFlag compressionMethod languageTag \0
			// translatedKeyword \0 text
			k := bytes.IndexByte(payload, 0)
			if k >= 0 && k+3 <= len(payload) && payload[k+1] == 0 {
				rest := payload[k+3:]
				if j := bytes.IndexByte(rest, 0); j >= 0 {
					rest = rest[j+1:]
					if m := bytes.IndexByte(rest, 0); m >= 0 {
						b.Write(rest[m+1:])
						b.WriteByte('\n')
					}
				}
			}
		case "IEND":
			return b.String()
		}
		i = end + 4 // skip the CRC
	}
	return b.String()
}

// imageDimensions pulls width/height from PNG, JPEG or WebP headers.
func imageDimensions(data []byte) (int, int, string, error) {
	switch {
	case len(data) >= 24 && string(data[1:4]) == "PNG":
		return int(be32(data[16:20])), int(be32(data[20:24])), "PNG", nil
	case len(data) >= 4 && data[0] == 0xFF && data[1] == 0xD8:
		if w, h, ok := jpegDimensions(data); ok {
			return w, h, "JPEG", nil
		}
		return 0, 0, "JPEG", fmt.Errorf("no SOF marker found")
	case len(data) >= 30 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return 0, 0, "WebP", fmt.Errorf("webp dimensions not decoded")
	}
	return 0, 0, "unknown", fmt.Errorf("unrecognised image format")
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func jpegDimensions(data []byte) (int, int, bool) {
	i := 2
	for i+9 < len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+9 < len(data) {
				h := int(data[i+5])<<8 | int(data[i+6])
				w := int(data[i+7])<<8 | int(data[i+8])
				return w, h, true
			}
			return 0, 0, false
		}
		if segLen <= 0 {
			return 0, 0, false
		}
		i += 2 + segLen
	}
	return 0, 0, false
}

// ---------------------------------------------------------------------------
// Financial documents
// ---------------------------------------------------------------------------

var (
	reInvoiceNo     = regexp.MustCompile(`(?im)^\s*(?:invoice|bill)\s*(?:no|number|#|id)?\s*[:.\-]?\s*([A-Za-z0-9][A-Za-z0-9\-\/]{2,})`)
	reVendor        = regexp.MustCompile(`(?im)^\s*(?:vendor|supplier|seller|billed\s*by|from)\s*[:.\-]?\s*(.+?)\s*$`)
	reDate          = regexp.MustCompile(`(?im)^\s*(?:date|issued(?:\s*on)?|invoice\s*date)\s*[:.\-]?\s*(\d{4}-\d{2}-\d{2}|\d{2}[\/\-]\d{2}[\/\-]\d{4})`)
	reTotal         = regexp.MustCompile(`(?im)^\s*(?:grand\s*total|total\s*amount|total|amount\s*due|amount)\s*[:.\-]?\s*((?:INR|USD|EUR|GBP|\x{20B9}|\$)?\s*[\d][\d,\s]*(?:\.\d{1,2})?)`)
	reQuantity      = regexp.MustCompile(`(?im)^\s*(?:qty|quantity|units?|no\.?\s*of\s*units?|count)\s*[:.\-]?\s*([\d][\d,]*)`)
	reUnitIDLn      = regexp.MustCompile(`(?im)^\s*(?:unit\s*ids?|serial\s*(?:no|numbers?)?|asset\s*ids?|device\s*ids?)\s*[:.\-]?\s*(.+)$`)
	reBeneficiaries = regexp.MustCompile(`(?im)^\s*(?:beneficiaries|households|families|people\s*served|persons?)\s*[:.\-]?\s*([\d][\d,]*)`)
	reVillages      = regexp.MustCompile(`(?im)^\s*(?:villages?|sites?|locations?)\s*[:.\-]?\s*(.+)$`)
)

func extractFinancialDocument(facts *models.ExtractedFacts, filename, contentType string, data []byte) {
	facts.DocumentType = "financial"
	text := documentText(filename, contentType, data)
	if strings.TrimSpace(text) == "" {
		facts.Quality = "insufficient"
		facts.ParseErrors = append(facts.ParseErrors,
			"No machine-readable text found in the document; the artefact cannot be reconciled against the milestone budget")
		return
	}

	facts.Quality = "ok"
	facts.InvoiceNumber = firstGroup(reInvoiceNo, text)
	facts.Vendor = firstGroup(reVendor, text)
	facts.DocumentDate = firstGroup(reDate, text)

	if raw := firstGroup(reTotal, text); raw != "" {
		if amt, ok := core.ParseAmount(raw); ok {
			facts.AmountMinor = int64(amt)
		} else {
			facts.ParseErrors = append(facts.ParseErrors, "Could not interpret total: "+raw)
		}
	}
	if raw := firstGroup(reQuantity, text); raw != "" {
		facts.StatedQuantity = atoiSafe(raw)
		facts.UnitLabel = "unit"
	}
	facts.UnitIDs = append(facts.UnitIDs, ScanUnitIDs(text)...)

	if facts.AmountMinor == 0 {
		facts.Notes = append(facts.Notes, "No total amount line was found")
	}
	if facts.StatedQuantity == 0 {
		facts.Notes = append(facts.Notes, "No quantity line was found")
	}
}

func extractDocument(facts *models.ExtractedFacts, filename, contentType string, data []byte) {
	facts.DocumentType = "document"
	text := documentText(filename, contentType, data)
	if strings.TrimSpace(text) == "" {
		facts.Quality = "insufficient"
		facts.ParseErrors = append(facts.ParseErrors, "Document contains no extractable text")
		return
	}
	facts.Quality = "ok"
	facts.Vendor = firstGroup(reVendor, text)
	facts.DocumentDate = firstGroup(reDate, text)
	if raw := firstGroup(reTotal, text); raw != "" {
		if amt, ok := core.ParseAmount(raw); ok {
			facts.AmountMinor = int64(amt)
		}
	}
	if raw := firstGroup(reQuantity, text); raw != "" {
		facts.StatedQuantity = atoiSafe(raw)
	}
	if raw := firstGroup(reBeneficiaries, text); raw != "" {
		facts.Beneficiaries = atoiSafe(raw)
	}
	if raw := firstGroup(reVillages, text); raw != "" {
		facts.Villages = splitList(raw)
	}
	facts.UnitIDs = append(facts.UnitIDs, ScanUnitIDs(text)...)
}

func extractRegistry(facts *models.ExtractedFacts, filename, contentType string, data []byte) {
	facts.DocumentType = "registry"
	text := documentText(filename, contentType, data)
	if strings.TrimSpace(text) == "" {
		facts.Quality = "insufficient"
		facts.ParseErrors = append(facts.ParseErrors, "Registry contains no extractable text")
		return
	}
	facts.Quality = "ok"
	facts.UnitIDs = ScanUnitIDs(text)
	if len(facts.UnitIDs) == 0 {
		facts.Quality = "low"
		facts.Notes = append(facts.Notes, "Registry did not contain any recognisable asset identifiers")
	}
	if raw := firstGroup(reQuantity, text); raw != "" {
		facts.StatedQuantity = atoiSafe(raw)
	}
}

func extractBeneficiaryRecord(facts *models.ExtractedFacts, filename, contentType string, data []byte) {
	facts.DocumentType = "beneficiary_record"
	text := documentText(filename, contentType, data)
	if strings.TrimSpace(text) == "" {
		facts.Quality = "insufficient"
		facts.ParseErrors = append(facts.ParseErrors, "Beneficiary record contains no extractable text")
		return
	}
	facts.Quality = "ok"
	if raw := firstGroup(reBeneficiaries, text); raw != "" {
		facts.Beneficiaries = atoiSafe(raw)
	}
	if raw := firstGroup(reVillages, text); raw != "" {
		facts.Villages = splitList(raw)
	}
	facts.Location = firstGroup(reVillages, text)

	// A beneficiary record is expected to carry one identifier per row.
	lines := nonEmptyLines(text)
	ids := []string{}
	for _, ln := range lines {
		if id := reRowID.FindString(ln); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		facts.UnitIDs = append(facts.UnitIDs, ids...)
		facts.Notes = append(facts.Notes, fmt.Sprintf("Counted %d beneficiary row identifier(s)", len(ids)))
	}
	if facts.Beneficiaries == 0 && len(ids) > 0 {
		facts.Beneficiaries = len(ids)
	}
}

var reRowID = regexp.MustCompile(`\b[A-Z]{2,6}-[A-Z0-9]{1,4}-?\d{2,6}\b`)

// ---------------------------------------------------------------------------
// Identifier scanning
// ---------------------------------------------------------------------------

// reUnitToken matches asset identifiers of the form CW-MH-0001, SUR-KAN-001 or
// UNIT/2026/0099.
//
// The shape is deliberately strict: two to eight leading letters, then a
// mandatory alphabetic group after a separator, then at most two further groups,
// and finally a numeric suffix. Requiring that second alphabetic segment is what
// keeps ordinary product model numbers such as "HF-100" from being counted as
// installed units, while still matching every serial scheme ImpactPay uses.
var reUnitToken = regexp.MustCompile(`\b[A-Z]{2,8}[-/_][A-Z]{2,6}(?:[-/_][A-Z0-9]{1,6}){0,2}[-/_]?\d{2,6}\b`)

// ScanUnitIDs extracts unique, ordered asset identifiers from free text.
func ScanUnitIDs(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range reUnitToken.FindAllString(text, -1) {
		id := strings.ToUpper(strings.TrimSpace(m))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Text handling
// ---------------------------------------------------------------------------

func documentText(filename, contentType string, data []byte) string {
	lower := strings.ToLower(filename)
	if strings.Contains(lower, ".json") || strings.Contains(contentType, "json") {
		if s := jsonToText(data); s != "" {
			return s + "\n" + printableText(data)
		}
	}
	return printableText(data)
}

// jsonToText flattens a JSON document into "key: value" lines so the same
// line-oriented parsers can read structured and unstructured artefacts alike.
func jsonToText(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return ""
	}
	var b strings.Builder
	flattenJSON(&b, "", v)
	return b.String()
}

func flattenJSON(b *strings.Builder, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			key := k
			if prefix != "" {
				key = prefix + "_" + k
			}
			flattenJSON(b, key, t[k])
		}
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			switch it := item.(type) {
			case string:
				parts = append(parts, it)
			case float64:
				parts = append(parts, strconv.FormatFloat(it, 'f', -1, 64))
			default:
				parts = append(parts, strings.TrimSpace(core.JSONString(it)))
			}
		}
		// Emit as a single line so the "unit ids: a, b, c" parser picks it up.
		fmt.Fprintf(b, "%s: %s\n", friendlyKey(prefix), strings.Join(parts, ", "))
	case string:
		fmt.Fprintf(b, "%s: %s\n", friendlyKey(prefix), t)
	case float64:
		fmt.Fprintf(b, "%s: %s\n", friendlyKey(prefix), strconv.FormatFloat(t, 'f', -1, 64))
	case bool:
		fmt.Fprintf(b, "%s: %t\n", friendlyKey(prefix), t)
	case nil:
	}
}

// friendlyKey converts snake_case JSON keys into the "unit ids" / "total amount"
// phrasing the line parsers recognise.
func friendlyKey(k string) string {
	k = strings.TrimPrefix(k, "_")
	if k == "" {
		return "value"
	}
	k = strings.ReplaceAll(k, "_", " ")
	k = strings.ReplaceAll(k, "total minor", "total")
	return k
}

// printableText returns the printable ASCII/UTF-8 runs of a byte slice.
func printableText(data []byte) string {
	if len(data) > MaxTextScan {
		data = data[:MaxTextScan]
	}
	var b strings.Builder
	b.Grow(len(data))
	run := 0
	for _, r := range string(data) {
		if r == '\n' || r == '\r' || r == '\t' || (r >= 32 && r < 127) {
			b.WriteRune(r)
			run = 0
			continue
		}
		run++
		if run > 4 {
			run = 0
		}
	}
	return b.String()
}

func firstGroup(re *regexp.Regexp, text string) string {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == '\t'
	})
	out := []string{}
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" && len(f) < 120 {
			out = append(out, f)
		}
	}
	return out
}

func nonEmptyLines(text string) []string {
	out := []string{}
	for _, ln := range strings.Split(text, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func atoiSafe(s string) int {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// Hint merging
// ---------------------------------------------------------------------------

func mergeHints(facts *models.ExtractedFacts, h models.EvidenceHints) {
	if len(h.UnitIDs) > 0 {
		facts.UnitIDs = dedupeAppend(facts.UnitIDs, h.UnitIDs)
		facts.Notes = append(facts.Notes,
			fmt.Sprintf("%d identifier(s) declared by the uploader", len(h.UnitIDs)))
	}
	if strings.TrimSpace(h.Location) != "" {
		facts.Location = strings.TrimSpace(h.Location)
	}
	if strings.TrimSpace(h.CapturedAt) != "" && facts.DocumentDate == "" {
		facts.DocumentDate = strings.TrimSpace(h.CapturedAt)
	}
	if len(h.Villages) > 0 && len(facts.Villages) == 0 {
		facts.Villages = append([]string{}, h.Villages...)
	}
	if h.AmountMinor > 0 && facts.AmountMinor == 0 {
		facts.AmountMinor = h.AmountMinor
		facts.Notes = append(facts.Notes, "Amount taken from uploader declaration (no amount found in the file)")
	}
	if h.Quantity > 0 && facts.StatedQuantity == 0 {
		facts.StatedQuantity = h.Quantity
	}
	if h.UnitLabel != "" && facts.UnitLabel == "" {
		facts.UnitLabel = h.UnitLabel
	}
	if h.InvoiceNo != "" && facts.InvoiceNumber == "" {
		facts.InvoiceNumber = h.InvoiceNo
	}
	if h.Vendor != "" && facts.Vendor == "" {
		facts.Vendor = h.Vendor
	}
	if h.Beneficiaries > 0 && facts.Beneficiaries == 0 {
		facts.Beneficiaries = h.Beneficiaries
	}
}

func dedupeAppend(base, add []string) []string {
	seen := map[string]bool{}
	for _, v := range base {
		seen[strings.ToUpper(strings.TrimSpace(v))] = true
	}
	out := append([]string{}, base...)
	for _, v := range add {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToUpper(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func finalise(facts *models.ExtractedFacts) {
	if facts.Quality == "" {
		facts.Quality = "ok"
	}
	if facts.UnitLabel == "" && len(facts.UnitIDs) > 0 {
		facts.UnitLabel = "unit"
	}
	facts.UnitIDs = dedupeAppend(nil, facts.UnitIDs)
	if facts.Notes == nil {
		facts.Notes = []string{}
	}
	if facts.ParseErrors == nil {
		facts.ParseErrors = []string{}
	}
}
