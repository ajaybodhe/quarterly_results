package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"quarterly_results/internal/domain"
)

// minimalResult returns an EarningsResult with enough fields populated to
// exercise the output formatters without causing zero-value formatting issues.
func minimalResult(symbol string) domain.EarningsResult {
	return domain.EarningsResult{
		Symbol:             symbol,
		CompanyName:        "Test Co",
		MarketCapB:         100.0,
		EarningsDate:       "2026-05-01",
		EarningsTime:       "amc",
		ResultDate:         "2026-05-02",
		FiscalQuarter:      "Mar/2026",
		EPSEstimate:        3.14,
		EPSPrevQtr:         "$3.00",
		EPSQoQ:             "+4.7%",
		EPSLastYear:        2.80,
		EPSYoYPct:          "+12.1%",
		RevEstimate:        "$15.00B",
		RevPrevQtr:         "$14.50B",
		RevQoQ:             "+3.4%",
		RevPrevYr:          "$13.00B",
		RevenueYoYPct:      "+15.4%",
		PE_TTM:             "25.1x",
		PE_Forward:         "22.3x",
		PS:                 "5.8x",
		CurrentPrice:       "$150.00",
		Ret1W:              "+1.2%",
		Ret1M:              "+3.5%",
		Ret6M:              "+18.0%",
		Ret1Y:              "+45.0%",
		InstActivity:       "Net Buyer",
		InstOwn:            "78.00%",
		InstTrans:          "+0.50%",
		ShortFloat:         "1.20%",
		ShortRatio:         "2.5d",
		InsiderActivity:    "Net Seller",
		InsiderBuyVal:      "$0",
		InsiderSellVal:     "$1.2M",
		InsiderNetVal:      "-$1.2M",
		InsiderFilings:     3,
		ConsensusRating:    "Buy",
		AvgPriceTarget:     "$180.00",
		PriceTargetUpside:  "+20.0%",
		AnalystBullish:     12,
		AnalystNeutral:     5,
		AnalystBearish:     2,
		AnalystTotal:       19,
		OptionsExpiry:      "2026-05-02",
		ExpectedMove:       "±$8.50",
		ExpectedMovePct:    "±5.7%",
		IVAtm:              "38.5%",
		PCVol:              "0.85",
		PCoi:               "0.92",
		Skew:               "-2.1%",
		MaxPain:            "$148.00",
		MaxPainVsCurrent:   "-1.3%",
		HistAvgAbsRxn:      "±4.8%",
		Hi52:               "$165.00",
		Lo52:               "$95.00",
		PctFrom52Hi:        "-9.1%",
		PctFrom52Lo:        "+57.9%",
		RSI14:              "58.3",
		ImpliedVsHistRatio: "1.19x",
		BeatRate:           "75%",
		AvgBeatPct:         "+8.2%",
	}
}

func TestWriteCSV_HeaderAndRow(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("TEST")
	writeCSV(&buf, []domain.EarningsResult{r})

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
	writeCSV(&buf, []domain.EarningsResult{minimalResult("A"), minimalResult("B"), minimalResult("C")})
	records, _ := csv.NewReader(&buf).ReadAll()
	// 1 header + 3 data rows.
	if len(records) != 4 {
		t.Errorf("expected 4 rows (header+3), got %d", len(records))
	}
}

func TestWriteJSON_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := []domain.EarningsResult{minimalResult("ROUND")}
	WriteJSON(&buf, in)

	var out []domain.EarningsResult
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
	WriteJSON(&buf, nil)
	if !strings.Contains(buf.String(), "null") && !strings.Contains(buf.String(), "[]") {
		t.Errorf("expected null or [] for empty results, got %q", buf.String())
	}
}

func TestWriteStockCard_ContainsKeyFields(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("CARD")
	r.MacroContext = "⚠ FOMC same-day"
	r.History = []domain.QuarterActual{
		{Period: "2025-12-31", PeriodStart: "2025-10-01", Revenue: 14e9, EPS: 3.00, FilingDate: "2026-02-15"},
	}
	r.MaterialEvents = []domain.MaterialEvent{
		{Date: "2026-03-10", Items: "5.02", Label: "Director/Officer Change", RetPct: -1.2, Abnormal: false},
		{Date: "2026-02-15", Items: "2.02,9.01", Label: "Earnings Release", RetPct: 4.5, Abnormal: true},
	}
	writeStockCard(&buf, r)
	out := buf.String()

	for _, want := range []string{
		"CARD",
		"Test Co",
		"3.14", // EPS estimate
		"FOMC", // macro context
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
	r.History = []domain.QuarterActual{
		{Period: "2025-03-31", PeriodStart: "2025-01-01", Revenue: 10e9, EPS: 2.0},
		{Period: "2025-06-30", PeriodStart: "2025-04-01", Revenue: 11e9, EPS: 2.2},
	}
	writeCSV(&buf, []domain.EarningsResult{r})
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
	r.EarningsReactions = []domain.EarningsReaction{
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
	WriteStockCards(&buf, []domain.EarningsResult{minimalResult("X"), minimalResult("Y")})
	out := buf.String()
	if !strings.Contains(out, " X ") {
		t.Errorf("missing symbol X in cards output")
	}
	if !strings.Contains(out, " Y ") {
		t.Errorf("missing symbol Y in cards output")
	}
}

func TestWriteStockCard_PeersTable(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("MSFT")
	beatPct := 3.5
	r.Peers = []domain.PeerResult{
		{
			Symbol:           "AAPL",
			CompanyName:      "Apple Inc.",
			MarketCapB:       3100,
			EarningsDate:     "2026-04-01",
			EarningsTime:     "amc",
			Period:           "2026-03-31",
			EPSEstimate:      1.62,
			EPSActual:        1.65,
			EPSBeatPct:       &beatPct,
			RevActual:        95.4e9,
			AnnouncementDate: "2026-04-01",
			ReactionDay:      "2026-04-02",
			PriorClose:       225.00,
			ReactionOpen:     228.50,
			ReactionClose:    230.00,
			GapRetPct:        1.56,
			DayRetPct:        2.22,
		},
	}
	writeStockCard(&buf, r)
	out := buf.String()

	for _, want := range []string{
		"Sector Peers",
		"AAPL",
		"amc",
		"2026-04-02", // reaction day
		"+1.6%",      // gap return
		"+2.2%",      // day return
		"$1.62",      // EPS estimate
		"$1.65",      // EPS actual
		"+3.5%",      // EPS beat
		"$95.40B",    // revenue actual
	} {
		if !strings.Contains(out, want) {
			t.Errorf("peer table missing %q in output", want)
		}
	}
}

func TestWriteStockCard_NoPeers(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("SOLO")
	r.Peers = nil
	writeStockCard(&buf, r)
	if strings.Contains(buf.String(), "Sector Peers") {
		t.Error("Sector Peers section should be absent when Peers is nil")
	}
}

func TestWriteStockCard_PeerNoEPSEstimate(t *testing.T) {
	// When EPSEstimate is 0 (not available), the column should show N/A.
	var buf bytes.Buffer
	r := minimalResult("X")
	r.Peers = []domain.PeerResult{
		{
			Symbol:        "Y",
			CompanyName:   "Y Corp",
			MarketCapB:    50,
			EarningsTime:  "bmo",
			Period:        "2026-03-31",
			EPSEstimate:   0, // unavailable
			EPSActual:     2.10,
			ReactionDay:   "2026-03-15",
			PriorClose:    100,
			ReactionClose: 105,
			DayRetPct:     5.0,
		},
	}
	writeStockCard(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "N/A") {
		t.Error("missing N/A for unavailable EPS estimate")
	}
}

func TestWriteStockCard_MaterialEventsAbnormalFlag(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("MAT")
	r.MaterialEvents = []domain.MaterialEvent{
		{Date: "2026-04-10", Items: "2.02,9.01", Label: "Earnings Release", RetPct: 8.5, Abnormal: true},
		{Date: "2026-03-15", Items: "5.02", Label: "Director/Officer Change", RetPct: -0.3, Abnormal: false},
	}
	writeStockCard(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "◀ abnormal") {
		t.Error("expected abnormal flag for high-return event")
	}
	if strings.Count(out, "◀ abnormal") != 1 {
		t.Errorf("expected exactly 1 abnormal flag, got %d", strings.Count(out, "◀ abnormal"))
	}
	if !strings.Contains(out, "Director/Officer Change") {
		t.Error("normal event label should still appear")
	}
	if !strings.Contains(out, "+8.5%") {
		t.Error("positive return should be formatted with + sign")
	}
	if !strings.Contains(out, "-0.3%") {
		t.Error("negative return should appear with - sign")
	}
}

func TestWriteStockCard_MaterialEventsZeroReturn(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("ZERO")
	r.MaterialEvents = []domain.MaterialEvent{
		// RetPct == 0 means price data was unavailable — display "—" not "+0.0%".
		{Date: "2026-04-01", Items: "1.01", Label: "Material Agreement", RetPct: 0, Abnormal: false},
	}
	writeStockCard(&buf, r)
	out := buf.String()
	if strings.Contains(out, "+0.0%") {
		t.Error("zero RetPct (unavailable) should render as — not +0.0%")
	}
	if !strings.Contains(out, "—") {
		t.Error("zero RetPct should render as — (em-dash)")
	}
}

func TestWriteStockCard_NoMaterialEvents(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("NONE")
	r.MaterialEvents = nil
	writeStockCard(&buf, r)
	if strings.Contains(buf.String(), "Material Events") {
		t.Error("Material Events section should be absent when slice is empty")
	}
}

func TestWriteStockCard_ForwardEPS(t *testing.T) {
	// ForwardEPS is shown inside the Quarterly History block only when History
	// is also present.
	var buf bytes.Buffer
	r := minimalResult("FWD")
	r.History = []domain.QuarterActual{
		{Period: "2025-12-31", PeriodStart: "2025-10-01", Revenue: 10e9, EPS: 3.0},
	}
	r.ForwardEPS = []domain.ForwardQuarter{
		{FiscalEnd: "Mar/2026", ConsensusEPS: 3.50, LowEPS: 3.20, HighEPS: 3.80, NumberOfEstimates: 20},
		{FiscalEnd: "Jun/2026", ConsensusEPS: 3.70, LowEPS: 3.40, HighEPS: 4.00, NumberOfEstimates: 18},
	}
	writeStockCard(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "Forward EPS") {
		t.Error("expected Forward EPS section when ForwardEPS is populated")
	}
	if !strings.Contains(out, "Mar/2026") {
		t.Error("expected first forward quarter period in output")
	}
}

func TestWriteStockCard_AnalystCountZero(t *testing.T) {
	// When AnalystTotal == 0, bullish/neutral/bearish should show "N/A",
	// not a divide-by-zero panic or percentage string.
	var buf bytes.Buffer
	r := minimalResult("NOANAL")
	r.AnalystTotal = 0
	r.AnalystBullish = 0
	r.AnalystNeutral = 0
	r.AnalystBearish = 0
	writeStockCard(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "N/A") {
		t.Error("expected N/A for analyst counts when total is 0")
	}
}

func TestWriteWebEarnings(t *testing.T) {
	// The agent's JSON for CDNS 2026-10-26 (trimmed), decoded with the Go contract.
	raw := `{"fiscal_quarter":"Q3 2026 (Sep/2026)","eps_actual":2.11,"eps_consensus":2.04,
	  "consensus_source":"Zacks","surprise_pct":3.43,"revenue_actual_b":1.633,"revenue_consensus_b":1.61,
	  "guidance":[
	    {"period":"Q4 2026","eps":{"low":2.19,"high":2.25},"revenue_b":{"low":1.695,"high":1.725},
	     "prior_eps":null,"prior_revenue_b":null,"consensus_eps":2.03,"consensus_revenue_b":1.65},
	    {"period":"FY 2026","eps":{"low":8.37,"high":8.43},"revenue_b":{"low":6.387,"high":6.417},
	     "prior_eps":{"low":8.05,"high":8.15},"prior_revenue_b":{"low":6.26,"high":6.34},
	     "consensus_eps":null,"consensus_revenue_b":null}],
	  "sources":["https://www.sec.gov/a.htm","https://finance.yahoo.com/b","https://www.sec.gov/c.htm"],
	  "notes":"","usable":true,"rejected_because":""}`
	var web domain.WebEarnings
	if err := json.Unmarshal([]byte(raw), &web); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writeWebEarnings(&buf, &web)
	out := buf.String()
	for _, want := range []string{
		"EPS $2.11 vs $2.04 Zacks (+3.4%)",
		"Rev $1.63B vs $1.61B (+1.4%)",
		"Guide Q4 2026: EPS $2.19–2.25 vs cons $2.03 (+9.4% at mid)",
		"Guide FY 2026: EPS $8.37–8.43, raised from $8.05–8.15",
		"Sources: sec.gov, finance.yahoo.com\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not used") {
		t.Errorf("usable result flagged:\n%s", out)
	}
	web.Usable, web.RejectedBecause = false, "no source URL"
	buf.Reset()
	writeWebEarnings(&buf, &web)
	if !strings.Contains(buf.String(), "⚠ not used for Signal 1: no source URL") {
		t.Errorf("rejection not shown:\n%s", buf.String())
	}
}
