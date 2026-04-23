package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

// minimalResult returns an EarningsResult with enough fields populated to
// exercise the output formatters without causing zero-value formatting issues.
func minimalResult(symbol string) EarningsResult {
	return EarningsResult{
		Symbol:          symbol,
		CompanyName:     "Test Co",
		MarketCapB:      100.0,
		EarningsDate:    "2026-05-01",
		EarningsTime:    "amc",
		ResultDate:      "2026-05-02",
		FiscalQuarter:   "Mar/2026",
		EPSEstimate:     3.14,
		EPSPrevQtr:      "$3.00",
		EPSQoQ:          "+4.7%",
		EPSLastYear:     2.80,
		EPSYoYPct:       "+12.1%",
		RevEstimate:     "$15.00B",
		RevPrevQtr:      "$14.50B",
		RevQoQ:          "+3.4%",
		RevPrevYr:       "$13.00B",
		RevenueYoYPct:   "+15.4%",
		PE_TTM:          "25.1x",
		PE_Forward:      "22.3x",
		PS:              "5.8x",
		CurrentPrice:    "$150.00",
		Ret1W:           "+1.2%",
		Ret1M:           "+3.5%",
		Ret6M:           "+18.0%",
		Ret1Y:           "+45.0%",
		InstActivity:    "Net Buyer",
		InstOwn:         "78.00%",
		InstTrans:       "+0.50%",
		ShortFloat:      "1.20%",
		ShortRatio:      "2.5d",
		InsiderActivity: "Net Seller",
		InsiderBuyVal:   "$0",
		InsiderSellVal:  "$1.2M",
		InsiderNetVal:   "-$1.2M",
		InsiderFilings:  3,
		ConsensusRating: "Buy",
		AvgPriceTarget:  "$180.00",
		PriceTargetUpside: "+20.0%",
		AnalystBullish:  12,
		AnalystNeutral:  5,
		AnalystBearish:  2,
		AnalystTotal:    19,
		OptionsExpiry:   "2026-05-02",
		ExpectedMove:    "±$8.50",
		ExpectedMovePct: "±5.7%",
		IVAtm:           "38.5%",
		PCVol:           "0.85",
		PCoi:            "0.92",
		Skew:            "-2.1%",
		MaxPain:         "$148.00",
		MaxPainVsCurrent: "-1.3%",
		HistAvgAbsRxn:   "±4.8%",
		Hi52:            "$165.00",
		Lo52:            "$95.00",
		PctFrom52Hi:     "-9.1%",
		PctFrom52Lo:     "+57.9%",
		RSI14:           "58.3",
		ImpliedVsHistRatio: "1.19x",
		BeatRate:        "75%",
		AvgBeatPct:      "+8.2%",
	}
}

func TestWriteCSV_HeaderAndRow(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("TEST")
	writeCSV(&buf, []EarningsResult{r})

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("CSV parse error: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + 1 data row, got %d rows", len(records))
	}
	// Header row must contain known columns.
	header := strings.Join(records[0], ",")
	for _, col := range []string{"symbol", "eps_estimate", "rev_estimate", "pe_ttm", "macro_context"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header missing %q", col)
		}
	}
	// Data row must have same column count as header.
	if len(records[1]) != len(records[0]) {
		t.Errorf("data row has %d columns, header has %d", len(records[1]), len(records[0]))
	}
	// Symbol in first data field.
	if records[1][0] != "TEST" {
		t.Errorf("first data field = %q, want TEST", records[1][0])
	}
}

func TestWriteCSV_MultipleRows(t *testing.T) {
	var buf bytes.Buffer
	writeCSV(&buf, []EarningsResult{minimalResult("A"), minimalResult("B"), minimalResult("C")})
	records, _ := csv.NewReader(&buf).ReadAll()
	// 1 header + 3 data rows.
	if len(records) != 4 {
		t.Errorf("expected 4 rows (header+3), got %d", len(records))
	}
}

func TestWriteJSON_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := []EarningsResult{minimalResult("ROUND")}
	writeJSON(&buf, in)

	var out []EarningsResult
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].Symbol != "ROUND" {
		t.Errorf("JSON round-trip mismatch: %+v", out)
	}
	if out[0].EPSEstimate != 3.14 {
		t.Errorf("EPSEstimate round-trip = %v, want 3.14", out[0].EPSEstimate)
	}
}

func TestWriteJSON_EmptySlice(t *testing.T) {
	var buf bytes.Buffer
	writeJSON(&buf, nil)
	if !strings.Contains(buf.String(), "null") && !strings.Contains(buf.String(), "[]") {
		t.Errorf("expected null or [] for empty results, got %q", buf.String())
	}
}

func TestWriteStockCard_ContainsKeyFields(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("CARD")
	r.MacroContext = "⚠ FOMC same-day"
	r.History = []QuarterActual{
		{Period: "2025-12-31", PeriodStart: "2025-10-01", Revenue: 14e9, EPS: 3.00, FilingDate: "2026-02-15"},
	}
	r.MaterialEvents = []MaterialEvent{
		{Date: "2026-03-10", Items: "5.02", Label: "Director/Officer Change", RetPct: -1.2, Abnormal: false},
		{Date: "2026-02-15", Items: "2.02,9.01", Label: "Earnings Release", RetPct: 4.5, Abnormal: true},
	}
	writeStockCard(&buf, r)
	out := buf.String()

	for _, want := range []string{
		"CARD",
		"Test Co",
		"3.14",    // EPS estimate
		"FOMC",    // macro context
		"Material Events",
		"Director/Officer Change",
		"Earnings Release",
		"◀ abnormal",
		"Quarterly History",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("writeStockCard output missing %q", want)
		}
	}
}

func TestWriteCSV_WithHistory(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("HIST")
	r.History = []QuarterActual{
		{Period: "2025-03-31", PeriodStart: "2025-01-01", Revenue: 10e9, EPS: 2.0},
		{Period: "2025-06-30", PeriodStart: "2025-04-01", Revenue: 11e9, EPS: 2.2},
	}
	writeCSV(&buf, []EarningsResult{r})
	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("CSV parse: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + data row")
	}
	// history_periods column should contain period dates joined by "|".
	row := strings.Join(records[1], ",")
	if !strings.Contains(row, "2025-03-31") || !strings.Contains(row, "2025-06-30") {
		t.Errorf("CSV row should contain period dates, got: %s", row)
	}
}

func TestWriteStockCard_WithEarningsReactions(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("RXN")
	v := 5.5
	beatPct := 10.2
	r.EarningsReactions = []EarningsReaction{
		{
			Period:           "2025-12-31",
			AnnouncementDate: "2026-01-28",
			ReactionDay:      "2026-01-29",
			PriorClose:       140.0,
			ReactionOpen:     145.0,
			ReactionClose:    148.0,
			GapRetPct:        3.57,
			RetPct:           5.71,
			EPSActual:        3.50,
			EPSEstimate:      3.14,
			EPSBeatPct:       &beatPct,
			RevenueActual:    15e9,
			Pre7Ret:          &v,
			Pre7Close:        138.0,
			Post7Ret:         &v,
			Post7Close:       152.0,
			VIX:              18.5,
			MacroContext:     "",
		},
	}
	writeStockCard(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "Past Earnings Reactions") {
		t.Errorf("output missing Past Earnings Reactions section")
	}
	if !strings.Contains(out, "2025-12-31") {
		t.Errorf("output missing quarter period")
	}
}

func TestWriteStockCard_NoMacro(t *testing.T) {
	// When MacroContext is empty, no "Macro" line should appear.
	var buf bytes.Buffer
	r := minimalResult("NOMAC")
	r.MacroContext = ""
	writeStockCard(&buf, r)
	if strings.Contains(buf.String(), "Macro  ") {
		t.Error("expected no Macro line when MacroContext is empty")
	}
}

func TestWriteStockCards_MultipleResults(t *testing.T) {
	var buf bytes.Buffer
	writeStockCards(&buf, []EarningsResult{minimalResult("X"), minimalResult("Y")})
	out := buf.String()
	if !strings.Contains(out, " X ") {
		t.Errorf("missing symbol X in cards output")
	}
	if !strings.Contains(out, " Y ") {
		t.Errorf("missing symbol Y in cards output")
	}
}
