// Package core holds small, dependency-free primitives shared across ImpactPay
// services: identifiers, clocks, money arithmetic, JSON marshalling helpers and
// content hashing.
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewID returns a new UUIDv4 string. All primary keys are application generated
// so the schema stays portable between PostgreSQL and SQLite.
func NewID() string { return uuid.NewString() }

// Now returns the current UTC time truncated to milliseconds so persisted
// timestamps round-trip exactly through RFC3339 strings.
func Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// NowStr returns the current UTC time as an RFC3339 string.
func NowStr() string { return FormatTime(Now()) }

// FormatTime renders a time as RFC3339 in UTC.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// ParseTime reads an RFC3339 timestamp, tolerating an empty string.
func ParseTime(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// MustParseTime returns the zero time when parsing fails.
func MustParseTime(s string) time.Time {
	t, err := ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// NullableTime converts the zero time into a SQL NULL-friendly nil.
func NullableTime(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// Token returns a cryptographically random URL-safe token.
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("core: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// RandomHex returns n random bytes hex encoded.
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("core: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// SHA256Hex hashes bytes for content-addressed evidence de-duplication.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// JSONString marshals a value to a JSON string, substituting "null" on failure.
func JSONString(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// MustJSON marshals and panics only on programmer error; used for seed data.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// DecodeJSON unmarshals a JSON string into dst, ignoring empty or malformed
// input so a single bad row never breaks a read path.
func DecodeJSON(s string, dst any) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return
	}
	_ = json.Unmarshal([]byte(s), dst)
}

// ---------------------------------------------------------------------------
// Money
// ---------------------------------------------------------------------------

// Money is an amount of minor currency units (paise for INR, cents for USD).
// Every persisted amount in ImpactPay is an integer in minor units so that no
// floating point rounding can corrupt a financial total.
type Money int64

// MajorUnits converts minor units to a decimal amount.
func (m Money) MajorUnits() float64 { return float64(m) / 100 }

// Add returns the sum of two amounts.
func (m Money) Add(o Money) Money { return m + o }

// Sub returns m-o, clamped at zero so a balance never goes negative by accident.
func (m Money) Sub(o Money) Money {
	if o > m {
		return 0
	}
	return m - o
}

// PercentOf returns the portion of m described by a 0..1 ratio.
func (m Money) PercentOf(ratio float64) Money {
	if ratio <= 0 {
		return 0
	}
	if ratio >= 1 {
		return m
	}
	return Money(int64(math.Round(float64(m) * ratio)))
}

// Proportion returns m * numerator / denominator using integer arithmetic.
func (m Money) Proportion(numerator, denominator int64) Money {
	if denominator == 0 {
		return 0
	}
	return Money(int64(math.Round(float64(m) * float64(numerator) / float64(denominator))))
}

var amountRe = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

// ParseAmount turns "150000", "1,50,000.50", "₹2,00,000" or "USD 1,000" into
// minor units. It returns false when no numeric value is present.
func ParseAmount(s string) (Money, bool) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ',', ' ', '\u00a0', '\u2009', '\u20b9', '$', '\u20ac', '\u00a3':
			return -1
		}
		return r
	}, s)
	// Strip a leading currency code like "INR" / "USD".
	cleaned = strings.TrimSpace(cleaned)
	if i := strings.IndexAny(cleaned, "0123456789"); i > 0 {
		cleaned = cleaned[i:]
	}
	m := amountRe.FindString(cleaned)
	if m == "" {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscanf(m, "%f", &f); err != nil {
		return 0, false
	}
	return Money(int64(math.Round(f * 100))), true
}

// FormatMoney renders a minor-unit amount with thousands separators and a
// currency symbol: 1000000 INR -> "₹10,000.00".
func FormatMoney(m Money, currency string) string {
	neg := m < 0
	if neg {
		m = -m
	}
	whole := int64(m) / 100
	frac := int64(m) % 100
	s := addThousands(whole)
	out := fmt.Sprintf("%s%s.%02d", symbol(currency), s, frac)
	if neg {
		return "-" + out
	}
	return out
}

// FormatMoneyShort renders a compact form used on project cards:
// 7200000 INR -> "₹72,000.00" style is too long, so this produces "₹72K"/"₹7.2L"
// for INR and "K"/"M" for other currencies.
func FormatMoneyShort(m Money, currency string) string {
	v := m.MajorUnits()
	sym := symbol(currency)
	if currency == "INR" {
		switch {
		case v >= 1e7:
			return fmt.Sprintf("%s%.2fCr", sym, v/1e7)
		case v >= 1e5:
			return trimZeros(fmt.Sprintf("%s%.1fL", sym, v/1e5))
		case v >= 1e3:
			return trimZeros(fmt.Sprintf("%s%.1fK", sym, v/1e3))
		default:
			return fmt.Sprintf("%s%.0f", sym, v)
		}
	}
	switch {
	case v >= 1e9:
		return trimZeros(fmt.Sprintf("%s%.1fB", sym, v/1e9))
	case v >= 1e6:
		return trimZeros(fmt.Sprintf("%s%.1fM", sym, v/1e6))
	case v >= 1e3:
		return trimZeros(fmt.Sprintf("%s%.1fK", sym, v/1e3))
	default:
		return fmt.Sprintf("%s%.2f", sym, v)
	}
}

func trimZeros(s string) string {
	s = strings.Replace(s, ".0", "", 1)
	return s
}

func symbol(currency string) string {
	switch strings.ToUpper(currency) {
	case "INR":
		return "\u20b9"
	case "USD":
		return "$"
	case "EUR":
		return "\u20ac"
	case "GBP":
		return "\u00a3"
	case "":
		return ""
	default:
		return strings.ToUpper(currency) + " "
	}
}

func addThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Text
// ---------------------------------------------------------------------------

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a title into a URL-safe slug.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugStrip.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// Truncate shortens s to at most n runes, appending an ellipsis.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "\u2026"
}

// Percent renders a 0..1 ratio as a whole-number percentage.
func Percent(ratio float64) int {
	return int(math.Round(ratio * 100))
}

// Ratio returns numerator/denominator guarded against division by zero.
func Ratio(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

// Clamp restricts v to the inclusive range [lo, hi].
func Clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
