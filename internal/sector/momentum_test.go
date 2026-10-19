package sector

import "testing"

func TestSICSectorGroup_Mapping(t *testing.T) {
	cases := []struct {
		sic  int
		want int
		desc string
	}{
		{3571, 8, "Apple (Electronic Computers) → same group as semiconductors"},
		{3674, 8, "NVIDIA (Semiconductors) → same group as hardware"},
		{7372, 8, "Microsoft (Prepackaged Software) → tech group via special case"},
		{7370, 8, "Google (Computer Services) → tech group via special case"},
		{6022, 13, "JPMorgan (Commercial Bank) → Finance group"},
		{6311, 13, "MetLife (Life Insurance) → Finance group"},
		{2836, 5, "Pfizer (Pharma) → Chemicals/Pharma group"},
		{4813, 10, "AT&T (Telephone) → Transportation group"},
		{5411, 12, "Kroger (Grocery) → Wholesale/Retail group"},
		{7011, 14, "Marriott (Hotels) → Hotels/Entertainment group"},
		{8011, 16, "UnitedHealth (Health Services) → Healthcare group"},
		{0, 0, "Unknown → 0"},
	}
	for _, tc := range cases {
		got := SICGroup(tc.sic)
		if got != tc.want {
			t.Errorf("sicSectorGroup(%d) [%s] = %d, want %d", tc.sic, tc.desc, got, tc.want)
		}
	}
}

func TestSICSectorGroup_TechPeersMatch(t *testing.T) {
	// AAPL (3571), MSFT (7372), GOOG (7370), NVDA (3674) must all be in the
	// same sector group so they surface as each other's peers.
	techSICs := []int{3571, 7372, 7370, 3674}
	group0 := SICGroup(techSICs[0])
	for _, sic := range techSICs[1:] {
		if g := SICGroup(sic); g != group0 {
			t.Errorf("tech SIC %d group %d ≠ AAPL group %d — they should be peers", sic, g, group0)
		}
	}
}

func TestSICSectorGroup_BankVsTechDiffer(t *testing.T) {
	techGroup := SICGroup(7372) // MSFT
	bankGroup := SICGroup(6022) // JPMorgan
	if techGroup == bankGroup {
		t.Errorf("tech and bank should be in different sectors (both got %d)", techGroup)
	}
}

func TestSectorETFForSIC_Overrides(t *testing.T) {
	cases := map[int]string{
		2834: "XLV",  // JNJ, PFE, MRK
		2836: "XBI",  // biological products
		6324: "XLV",  // UNH
		6022: "KBE",  // state commercial banks
		6211: "XLF",  // broker-dealers (GS)
		6798: "XLRE", // REITs
		4813: "XLC",  // telecom
		5331: "XLP",  // variety stores (WMT, COST)
		5961: "XLY",  // catalog & mail-order (AMZN)
		8711: "XLI",  // engineering services
		8062: "XLV",  // hospitals
		3674: "SOXX", // semiconductors
		7372: "XLK",  // software
	}
	for sic, want := range cases {
		if got := sectorETFForSIC(sic); got != want {
			t.Errorf("sectorETFForSIC(%d) = %s, want %s", sic, got, want)
		}
	}
}
