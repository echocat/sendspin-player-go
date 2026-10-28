package protocol

import "strings"

// WellFormedLanguageTag checks RFC 5646 section 2.1 syntax and the duplicate
// variant/singleton restrictions of section 2.2.9. It deliberately does not
// require a particular IANA registry version, validate extension semantics, or
// canonicalize the tag: server language preferences are informational hints.
func WellFormedLanguageTag(tag string) bool {
	// Validate ASCII, separators and subtag lengths before case folding. This
	// also avoids allocating a slice proportional to the number of subtags.
	length := 0
	for i := range len(tag) {
		c := tag[i]
		if c == '-' {
			if length == 0 {
				return false
			}
			length = 0
			continue
		}
		if !asciiLetter(c) && !asciiDigit(c) {
			return false
		}
		length++
		if length > 8 {
			return false
		}
	}
	if length == 0 {
		return false
	}
	tag = strings.ToLower(tag)
	// The 17 irregular grandfathered tags are the fixed exception to the
	// ordinary grammar. The other nine grandfathered tags already match it.
	switch tag {
	case "en-gb-oed", "i-ami", "i-bnn", "i-default", "i-enochian", "i-hak", "i-klingon", "i-lux", "i-mingo", "i-navajo", "i-pwn", "i-tao", "i-tay", "i-tsu", "sgn-be-fr", "sgn-be-nl", "sgn-ch-de":
		return true
	}
	part, rest, _ := strings.Cut(tag, "-")
	next := func() { part, rest, _ = strings.Cut(rest, "-") }
	if part == "x" {
		next()
		return part != ""
	}
	primaryLength := len(part)
	if primaryLength < 2 || !asciiLetters(part) {
		return false
	}
	next()
	if primaryLength <= 3 {
		for count := 0; count < 3 && len(part) == 3 && asciiLetters(part); count++ {
			next()
		}
	}
	if len(part) == 4 && asciiLetters(part) {
		next()
	} // script
	if (len(part) == 2 && asciiLetters(part)) || (len(part) == 3 && asciiDigits(part)) {
		next()
	} // region
	var variants map[string]bool
	for len(part) >= 5 || (len(part) == 4 && asciiDigit(part[0])) {
		if variants == nil {
			variants = map[string]bool{}
		}
		if variants[part] {
			return false
		}
		variants[part] = true
		next()
	}
	var singletons uint64
	for len(part) == 1 && part != "x" {
		var index byte
		if asciiDigit(part[0]) {
			index = part[0] - '0'
		} else {
			index = part[0] - 'a' + 10
		}
		bit := uint64(1) << index
		if singletons&bit != 0 {
			return false
		}
		singletons |= bit
		next()
		if len(part) < 2 {
			return false
		}
		for len(part) >= 2 {
			next()
		}
	}
	if part == "x" {
		next()
		return part != ""
	}
	return part == ""
}

func asciiLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func asciiDigit(c byte) bool  { return c >= '0' && c <= '9' }
func asciiLetters(s string) bool {
	for i := range len(s) {
		if !asciiLetter(s[i]) {
			return false
		}
	}
	return s != ""
}
func asciiDigits(s string) bool {
	for i := range len(s) {
		if !asciiDigit(s[i]) {
			return false
		}
	}
	return s != ""
}
