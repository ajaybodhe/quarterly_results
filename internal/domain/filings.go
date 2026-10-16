package domain

// InsiderSummary holds aggregated open-market insider transaction data.
type InsiderSummary struct {
	BuyShares   int64
	BuyValue    float64 // USD total of open-market purchases
	SellShares  int64
	SellValue   float64 // USD total of open-market sales
	FilingCount int     // number of Form 4 filings processed
	Activity    string  // "Net Buyer" / "Net Seller" / "No Activity"
}

// MaterialEvent is a significant 8-K filing with optional stock-price context.
type MaterialEvent struct {
	Date      string  // YYYY-MM-DD (8-K filing date)
	Items     string  // raw item string, e.g. "2.02,9.01"
	Label     string  // human-readable label for the most significant item
	DocDesc   string  // SEC primary-document description, e.g. "EARNINGS RELEASE"
	Snippet   string  // first ~400 chars of meaningful text from the 8-K document
	Sentiment string  // "Positive" / "Negative" / "Neutral" (keyword-based)
	RetPct    float64 // stock return on that date (0 = unavailable)
	Abnormal  bool    // |RetPct| > 1.5× 30-day rolling daily vol
}
