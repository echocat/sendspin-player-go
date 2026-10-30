package mdnstest

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var e2eTestNames = [...]string{
	"TestE2EMDNSIPv6ScopedDiscoveryAndEncryptedSession",
	"TestE2EMDNSIPv6ServerInitiatedDiscovery",
	"TestE2EMDNSClientInitiatedDiscoveryAndModeReservation",
	"TestE2EMDNSServerInitiatedUniqueInstancesAndGoodbye",
	"TestE2EMDNSLongFriendlyNameRemainsUsable",
	"TestE2EMDNSMissingPathIsIgnoredAndCancellationReleasesMode",
	"TestE2EMDNSRepeatedCanceledBrowsersReleaseSockets",
	"TestE2EMDNSActualInterfaceAttachAndRemoval",
}

// selectedE2E only decides whether a top-level E2E body can run. Go still runs
// that body when -run names a nonexistent subtest, and a partial -skip must not
// suppress it. Actual test execution and subtest filtering remain testing's job.
func selectedE2E(run, skip string) (bool, error) {
	include, err := compileTestFilter(run)
	if err != nil {
		return false, err
	}
	var exclude testFilter
	if skip != "" {
		exclude, err = compileTestFilter(skip)
		if err != nil {
			return false, err
		}
	}
	for _, name := range e2eTestNames {
		if match, _ := include.matchesRoot(name); !match {
			continue
		}
		if match, partial := exclude.matchesRoot(name); !match || partial {
			return true, nil
		}
	}
	return false, nil
}

type testFilter [][]*regexp.Regexp

func (f testFilter) matchesRoot(name string) (match, partial bool) {
	for _, branch := range f {
		if branch[0].MatchString(name) {
			// Like testing's alternation matcher, the first matching branch
			// decides whether this is a partial match, even for -skip.
			return true, len(branch) > 1
		}
	}
	return false, false
}

// Go treats only unescaped '/' and '|' outside brackets and parentheses as
// separators. Compile every segment, including subtest segments: malformed
// filters must be reported by m.Run on the host, not trigger fixture setup.
func compileTestFilter(pattern string) (testFilter, error) {
	var result testFilter
	var branch []*regexp.Regexp
	start, brackets, parentheses := 0, 0, 0
	for i := 0; i <= len(pattern); i++ {
		separator := i == len(pattern)
		if !separator {
			switch pattern[i] {
			case '\\':
				if i+1 < len(pattern) {
					i++
					continue
				}
			case '[':
				brackets++
			case ']':
				brackets = max(0, brackets-1)
			case '(':
				if brackets == 0 {
					parentheses++
				}
			case ')':
				if brackets == 0 {
					parentheses--
				}
			case '/', '|':
				separator = brackets == 0 && parentheses == 0
			}
		}
		if !separator {
			continue
		}
		re, err := regexp.Compile(rewriteTestFilter(pattern[start:i]))
		if err != nil {
			return nil, err
		}
		branch = append(branch, re)
		if i == len(pattern) || pattern[i] == '|' {
			result = append(result, branch)
			branch = nil
		}
		start = i + 1
	}
	return result, nil
}

func rewriteTestFilter(pattern string) string {
	var result strings.Builder
	for _, r := range pattern {
		switch {
		case unicode.IsSpace(r):
			result.WriteByte('_')
		case !strconv.IsPrint(r):
			quoted := strconv.QuoteRune(r)
			result.WriteString(quoted[1 : len(quoted)-1])
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}
