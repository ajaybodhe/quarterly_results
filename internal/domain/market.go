package domain

// PeerResult holds earnings results for one sector peer that already reported
// the same fiscal quarter.
type PeerResult struct {
	Symbol       string  `json:"symbol"`
	CompanyName  string  `json:"company_name"`
	MarketCapB   float64 `json:"market_cap_b"`
	EarningsDate string  `json:"earnings_date"` // YYYY-MM-DD
	EarningsTime string  `json:"earnings_time"` // "bmo" / "amc" / ""
	Period       string  `json:"period"`        // fiscal quarter end matched to target

	// Consensus EPS estimate vs actual
	EPSEstimate float64  `json:"eps_estimate"`
	EPSActual   float64  `json:"eps_actual"`
	EPSBeatPct  *float64 `json:"eps_beat_pct,omitempty"` // (actual−est)/|est|×100

	// Revenue actual (consensus revenue estimate not available historically)
	RevActual float64 `json:"rev_actual"`

	// Price reaction — reaction day is BMO/AMC-aware (see comment on reactionDayFor)
	AnnouncementDate string  `json:"announcement_date"` // 8-K date or earnings date
	ReactionDay      string  `json:"reaction_day"`      // YYYY-MM-DD
	PriorClose       float64 `json:"prior_close"`       // close before market saw results
	ReactionOpen     float64 `json:"reaction_open"`     // first open after results known
	ReactionClose    float64 `json:"reaction_close"`    // close on reaction day
	GapRetPct        float64 `json:"gap_ret_pct"`       // (open − prior) / prior × 100
	DayRetPct        float64 `json:"day_ret_pct"`       // (close − prior) / prior × 100

	// Valuation ratios at the time of the peer's report — used to compute the
	// industry median PE/PS for the target stock's score (signal `pe_vs_industry`).
	// Both 0 if not computable from available data.
	PE_TTM float64 `json:"pe_ttm,omitempty"`
	PS     float64 `json:"ps,omitempty"`
}

// OptionsSnapshot holds pre-earnings options analytics for one stock.
// All computed from the nearest options expiry that falls on or after the earnings date,
// so the straddle price fully captures the expected earnings move.
type OptionsSnapshot struct {
	Expiry           string  // YYYY-MM-DD of the expiry used
	ExpectedMove     float64 // ATM straddle mid-price ($): call_mid + put_mid
	ExpectedMovePct  float64 // ExpectedMove / stock_price × 100
	IVAtm            float64 // average IV of ATM call + put (in %, e.g. 84.2)
	PCVol            float64 // total put volume / total call volume
	PCoi             float64 // total put OI / total call OI
	Skew             float64 // IV(5%-OTM put) − IV(5%-OTM call), in %
	MaxPain          float64 // strike price that minimises option-buyer payouts ($)
	MaxPainVsCurrent float64 // (MaxPain − stock_price) / stock_price × 100
	TotalCallOI      int
	TotalPutOI       int
	TotalCallVol     int
	TotalPutVol      int
	HistAvgAbsRxn    float64      // average |RXN_RET| across historical quarters (%) — for comparison
	GEX              *GEXSnapshot // populated only when --gex flag is set
}

// GEXSnapshot holds dealer gamma exposure computed from the options chain.
// GEX = Σ (γ_call × OI_call − γ_put × OI_put) × 100 × spot.
// Positive GEX: dealers are long gamma → dampen moves (buy dips, sell rallies).
// Negative GEX: dealers are short gamma → amplify moves (chase the direction).
type GEXSnapshot struct {
	NetGEX   float64       // total net GEX in USD
	Signal   string        // "Positive (dampening)" or "Negative (amplifying)"
	ByStrike []GEXByStrike // top 15 strikes by |NetGEX|, sorted descending
}

// GEXByStrike is one row in the GEX by-strike breakdown.
type GEXByStrike struct {
	Strike  float64
	CallGEX float64
	PutGEX  float64
	NetGEX  float64
}

// InstitutionalData holds institutional ownership metrics from Finviz.
// "Inst Trans" represents the net % change in institutional holdings from the
// prior 13F quarter to the most recent one — positive = net buying, negative = net selling.
type InstitutionalData struct {
	InstOwn    float64 // current institutional ownership as % of shares outstanding
	InstTrans  float64 // quarter-over-quarter change in institutional ownership (%)
	Activity   string  // "Net Buyer" / "Net Seller" / "No Activity"
	ShortFloat float64 // short interest as % of float
	ShortRatio float64 // days-to-cover (short interest / avg daily volume)
}
