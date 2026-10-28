package protocol

import (
	"regexp"
	"strings"
	"testing"
)

// Independent test-only transcription of RFC 5646 section 2.1's ABNF, not
// generated from the production scanner. Duplicate variants/singletons are the
// additional section 2.2.9 checks; no IANA/extension semantic lookup is implied.
var languageTagABNF = regexp.MustCompile(`^(?:` +
	`(?:[a-z]{2,3}(?:-[a-z]{3}){0,3}|[a-z]{4}|[a-z]{5,8})` +
	`(?:-[a-z]{4})?(?:-(?:[a-z]{2}|[0-9]{3}))?` +
	`(?P<variants>(?:-(?:[a-z0-9]{5,8}|[0-9][a-z0-9]{3}))*)` +
	`(?P<extensions>(?:-[0-9a-wy-z](?:-[a-z0-9]{2,8})+)*)` +
	`(?:-x(?:-[a-z0-9]{1,8})+)?|` +
	`x(?:-[a-z0-9]{1,8})+|` +
	`en-gb-oed|i-(?:ami|bnn|default|enochian|hak|klingon|lux|mingo|navajo|pwn|tao|tay|tsu)|sgn-(?:be-fr|be-nl|ch-de)` +
	`)$`)

func referenceLanguageTag(tag string) bool {
	for _, c := range []byte(tag) {
		if c >= 128 {
			return false
		}
	}
	parts := languageTagABNF.FindStringSubmatch(strings.ToLower(tag))
	if parts == nil {
		return false
	}
	for _, group := range []string{"variants", "extensions"} {
		seen := map[string]bool{}
		for _, part := range strings.Split(parts[languageTagABNF.SubexpIndex(group)], "-") {
			if part == "" || group == "extensions" && len(part) != 1 {
				continue
			}
			if seen[part] {
				return false
			}
			seen[part] = true
		}
	}
	return true
}

func TestLanguageTagRFC5646Examples(t *testing.T) {
	// RFC 5646 section 2.1's complete grandfathered list and Appendix A
	// examples. Registration/extension semantics are deliberately not checked.
	for _, tag := range []string{
		"de", "fr", "ja", "i-enochian", "zh-Hant", "zh-Hans", "sr-Cyrl", "sr-Latn",
		"zh-cmn-Hans-CN", "cmn-Hans-CN", "zh-yue-HK", "yue-HK", "zh-Hans-CN", "sr-Latn-RS",
		"sl-rozaj", "sl-rozaj-biske", "sl-nedis", "de-CH-1901", "sl-IT-nedis", "hy-Latn-IT-arevela",
		"de-DE", "en-US", "es-419", "de-CH-x-phonebk", "az-Arab-x-AZE-derbend", "x-whatever", "qaa-Qaaa-QM-x-southern",
		"de-Qaaa", "sr-Latn-QM", "sr-Qaaa-RS", "en-US-u-islamcal", "zh-CN-a-myext-x-private", "en-a-myext-b-another",
		"en-GB-oed", "i-ami", "i-bnn", "i-default", "i-hak", "i-klingon", "i-lux", "i-mingo", "i-navajo", "i-pwn", "i-tao", "i-tay", "i-tsu",
		"sgn-BE-FR", "sgn-BE-NL", "sgn-CH-DE", "art-lojban", "cel-gaulish", "no-bok", "no-nyn", "zh-guoyu", "zh-hakka", "zh-min", "zh-min-nan", "zh-xiang",
		// Syntactically sound future/private subtags must not depend on an old
		// registry snapshot. Duplicate full tags in a preference list are legal.
		"abcd", "abcde", "abcdefgh", "zz-Zzzz-999-future1", "aa-bbb-ccc-ddd", "en-0-ab", "x-a-1-b", "en-x-a-a", "en-u-ca-gregory-ca-buddhist",
	} {
		if !WellFormedLanguageTag(tag) || !WellFormedLanguageTag(strings.ToUpper(tag)) {
			t.Errorf("rejected well-formed tag %q", tag)
		}
	}
}

func TestLanguageTagRejectsMalformedStructures(t *testing.T) {
	for _, tag := range []string{
		"", "a", "i-future", "123", "en_US", " de", "de ", "de\tDE", "en/US", "en.*", "en-\u212a", "en-\u00e9", "\xff",
		"-en", "en-", "en--US", "abcdefghi", "en-abcdefghi", "en-a", "en-a-x-private", "x", "en-x", "en-Latn-Cyrl", "en-US-GB",
		"aa-bbb-ccc-ddd-eee", "abcd-eee", "en-abc-1234-US", "en-variant-VARIANT", "de-1901-1901", "en-a-foo-A-bar", "en-0-foo-0-bar",
	} {
		if WellFormedLanguageTag(tag) {
			t.Errorf("accepted malformed tag %q", tag)
		}
	}
}

func TestLanguageTagHasNoArbitraryPrivateUseLengthCap(t *testing.T) {
	if !WellFormedLanguageTag("x-" + strings.Repeat("abcdefgh-", 1000) + "last") {
		t.Fatal("long but structurally valid private tag rejected")
	}
	if WellFormedLanguageTag("en-" + strings.Repeat("-", 100000)) {
		t.Fatal("empty subtags accepted")
	}
}

func FuzzLanguageTag(f *testing.F) {
	for _, tag := range []string{"de-DE", "i-klingon", "x-private", "en-a-foo-a-bar", "en_US", "en-variant-variant", "zh-cmn-Hans-CN"} {
		f.Add(tag)
	}
	f.Fuzz(func(t *testing.T, tag string) {
		if len(tag) > 128<<10 {
			return
		}
		valid := WellFormedLanguageTag(tag)
		if valid != referenceLanguageTag(tag) {
			t.Fatalf("scanner differs from RFC ABNF oracle for %q", tag)
		}
		ascii := true
		for _, c := range []byte(tag) {
			if c >= 128 {
				ascii = false
				break
			}
		}
		if ascii && valid != WellFormedLanguageTag(strings.ToUpper(tag)) {
			t.Fatal("case-sensitive language validation")
		}
		if valid {
			if tag == "" || strings.HasPrefix(tag, "-") || strings.HasSuffix(tag, "-") || strings.Contains(tag, "--") {
				t.Fatal("accepted empty subtag")
			}
			for _, c := range []byte(tag) {
				if !asciiLetter(c) && !asciiDigit(c) && c != '-' {
					t.Fatal("accepted non-BCP47 character")
				}
			}
		}
	})
}
