package mdnstest

import "testing"

func TestSelectionRespectsGoRunAndSkipFilters(t *testing.T) {
	for _, test := range []struct {
		name, run, skip string
		want            bool
	}{
		{name: "default", want: true},
		{name: "smoke", run: "^$"},
		{name: "unit only", run: "^TestIndependent"},
		{name: "unit subtest", run: "^TestIndependentDNSViewExpiresRecordsAndKeepsTXTOptional$/child"},
		{name: "all E2E", run: "^TestE2EMDNS", want: true},
		{name: "one E2E", run: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$", want: true},
		{name: "nonexistent E2E", run: "^TestE2EMDNSNotPresent$"},
		{name: "nonexistent subtest still runs parent", run: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$/^NotPresent$", want: true},
		{name: "subtest name cannot select parent", run: "^TestIndependent$/TestE2EMDNS"},
		{name: "empty parent filter", run: "/child", want: true},
		{name: "top level alternative after subtest", run: "^TestIndependent$/child|^TestE2EMDNSIPv6ServerInitiatedDiscovery$", want: true},
		{name: "parenthesized slash not a separator", run: "^(TestIndependent/child|TestE2EMDNSIPv6ServerInitiatedDiscovery)$", want: true},
		{name: "bracket separators", run: "^TestE2EMDNS[/|]NotPresent$"},
		{name: "escaped slash", run: `^TestIndependent\/child|^TestE2EMDNSIPv6ServerInitiatedDiscovery$`, want: true},
		{name: "skip all", skip: "^TestE2EMDNS"},
		{name: "skip selected parent", run: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$", skip: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$"},
		{name: "skip only child", run: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$", skip: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$/child", want: true},
		{name: "first partial skip alternative wins", run: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$", skip: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$/child|^TestE2EMDNS", want: true},
		{name: "first complete skip alternative wins", run: "^TestE2EMDNSIPv6ServerInitiatedDiscovery$", skip: "^TestE2EMDNS|^TestE2EMDNSIPv6ServerInitiatedDiscovery$/child"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectedE2E(test.run, test.skip)
			if err != nil || got != test.want {
				t.Fatalf("selectedE2E(%q, %q) = %v, %v; want %v", test.run, test.skip, got, err, test.want)
			}
		})
	}
}

func TestSelectionRejectsInvalidFiltersBeforeFixtureSetup(t *testing.T) {
	for _, pattern := range []string{"[", "unit/[", "unit/child|[", `unit/\`} {
		if _, err := selectedE2E(pattern, ""); err == nil {
			t.Errorf("accepted invalid run filter %q", pattern)
		}
		if _, err := selectedE2E("^$", pattern); err == nil {
			t.Errorf("accepted invalid skip filter %q", pattern)
		}
	}
}
