package collector

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var suffixRegex = regexp.MustCompile(`(?i)\s+(SAS|SARL|SASU|SA|SNC|SCI|EURL|SELARL|EI)$`)

// cleanCompanyName strips legal suffixes like "SAS", "SARL", etc.
func cleanCompanyName(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = suffixRegex.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

// normalizeName lowercases, removes accents, and collapses whitespace.
func normalizeName(s string) string {
	// Lowercase
	s = strings.ToLower(s)

	// Remove accents
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	normalized, _, _ := transform.String(t, s)

	// Remove non-alphanumeric characters (keep spaces)
	reg := regexp.MustCompile(`[^a-z0-9\s]+`)
	normalized = reg.ReplaceAllString(normalized, " ")

	// Collapse whitespace
	normalized = strings.Join(strings.Fields(normalized), " ")

	return normalized
}
