package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// InstitutionalData holds institutional ownership metrics from Finviz.
// "Inst Trans" represents the net % change in institutional holdings from the
// prior 13F quarter to the most recent one — positive = net buying, negative = net selling.
type InstitutionalData struct {
	InstOwn   float64 // current institutional ownership as % of shares outstanding
	InstTrans float64 // quarter-over-quarter change in institutional ownership (%)
	Activity  string  // "Net Buyer" / "Net Seller" / "No Activity"
}

// finvizFieldRe matches a metric name and its bold value from the Finviz snapshot table.
var finvizFieldRe = regexp.MustCompile(`>([^<]{1,40})</(?:td|a)>\s*<td[^>]*><b>([^<]+)</b>`)

// fetchInstitutionalData fetches institutional ownership data from Finviz.
func (e *Enricher) fetchInstitutionalData(symbol string) (*InstitutionalData, error) {
	url := fmt.Sprintf("https://finviz.com/quote.ashx?t=%s&p=d", symbol)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	// Small delay to avoid hammering Finviz under concurrent load.
	time.Sleep(200 * time.Millisecond)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("finviz HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseFinvizData(string(body))
}

func parseFinvizData(html string) (*InstitutionalData, error) {
	// Build a map of all metric-name → value pairs from the snapshot table.
	metrics := make(map[string]string)
	for _, m := range finvizFieldRe.FindAllStringSubmatch(html, -1) {
		name := strings.TrimSpace(m[1])
		val := strings.TrimSpace(m[2])
		if name != "" && val != "" {
			metrics[name] = val
		}
	}

	instOwnStr := strings.TrimSuffix(strings.TrimSpace(metrics["Inst Own"]), "%")
	instTransStr := strings.TrimSuffix(strings.TrimSpace(metrics["Inst Trans"]), "%")

	if instOwnStr == "" {
		return nil, fmt.Errorf("institutional data not found in page")
	}

	instOwn, _ := strconv.ParseFloat(instOwnStr, 64)
	instTrans, _ := strconv.ParseFloat(instTransStr, 64)

	var activity string
	switch {
	case instTrans > 0:
		activity = "Net Buyer"
	case instTrans < 0:
		activity = "Net Seller"
	default:
		activity = "No Activity"
	}

	return &InstitutionalData{
		InstOwn:   instOwn,
		InstTrans: instTrans,
		Activity:  activity,
	}, nil
}
