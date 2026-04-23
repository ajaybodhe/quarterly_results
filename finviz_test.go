package main

import (
	"math"
	"strings"
	"testing"
)

// finvizFieldHTML generates a snippet of the Finviz snapshot table HTML for one metric.
// The regex in parseFinvizData requires the label <div>, closing </div></td>, then a
// <td>...<div>...<b>VALUE</b> block.
func finvizFieldHTML(label, value string) string {
	return `<td><div class="snapshot-td-label">` + label + `</div></td>` +
		`<td class="snapshot-td-content-cell"><div class="snapshot-td-content"><b>` + value + `</b></div></td>`
}

func finvizPageHTML(fields map[string]string) string {
	var sb strings.Builder
	sb.WriteString(`<html><body><table class="snapshot-table2"><tr>`)
	for k, v := range fields {
		sb.WriteString(finvizFieldHTML(k, v))
	}
	sb.WriteString(`</tr></table></body></html>`)
	return sb.String()
}

func TestParseFinvizData_NetBuyer(t *testing.T) {
	html := finvizPageHTML(map[string]string{
		"Inst Own":    "81.01%",
		"Inst Trans":  "0.35%",
		"Short Float": "1.20%",
		"Short Ratio": "2.50",
	})
	got, err := parseFinvizData(html)
	if err != nil {
		t.Fatalf("parseFinvizData: %v", err)
	}
	if math.Abs(got.InstOwn-81.01) > 1e-6 {
		t.Errorf("InstOwn = %v, want 81.01", got.InstOwn)
	}
	if math.Abs(got.InstTrans-0.35) > 1e-6 {
		t.Errorf("InstTrans = %v, want 0.35", got.InstTrans)
	}
	if got.Activity != "Net Buyer" {
		t.Errorf("Activity = %q, want Net Buyer (positive Inst Trans)", got.Activity)
	}
	if math.Abs(got.ShortFloat-1.20) > 1e-6 {
		t.Errorf("ShortFloat = %v, want 1.20", got.ShortFloat)
	}
	if math.Abs(got.ShortRatio-2.50) > 1e-6 {
		t.Errorf("ShortRatio = %v, want 2.50", got.ShortRatio)
	}
}

func TestParseFinvizData_NetSeller(t *testing.T) {
	html := finvizPageHTML(map[string]string{
		"Inst Own":   "70.00%",
		"Inst Trans": "-1.25%",
	})
	got, err := parseFinvizData(html)
	if err != nil {
		t.Fatalf("parseFinvizData: %v", err)
	}
	if got.Activity != "Net Seller" {
		t.Errorf("Activity = %q, want Net Seller (negative Inst Trans)", got.Activity)
	}
}

func TestParseFinvizData_NoActivity(t *testing.T) {
	html := finvizPageHTML(map[string]string{
		"Inst Own":   "70.00%",
		"Inst Trans": "0.00%",
	})
	got, err := parseFinvizData(html)
	if err != nil {
		t.Fatalf("parseFinvizData: %v", err)
	}
	if got.Activity != "No Activity" {
		t.Errorf("Activity = %q, want No Activity (zero Inst Trans)", got.Activity)
	}
}

func TestParseFinvizData_MissingInstOwn(t *testing.T) {
	// No "Inst Own" field at all → error.
	html := finvizPageHTML(map[string]string{
		"Inst Trans": "0.35%",
	})
	_, err := parseFinvizData(html)
	if err == nil {
		t.Error("expected error when Inst Own is missing")
	}
}

func TestParseFinvizData_UnparseableValues(t *testing.T) {
	// Short Float = "-" (Finviz uses "-" for unknown values); parser must not crash,
	// just fall back to zero.
	html := finvizPageHTML(map[string]string{
		"Inst Own":    "50.00%",
		"Inst Trans":  "0.10%",
		"Short Float": "-",
		"Short Ratio": "-",
	})
	got, err := parseFinvizData(html)
	if err != nil {
		t.Fatalf("parseFinvizData: %v", err)
	}
	if got.ShortFloat != 0 || got.ShortRatio != 0 {
		t.Errorf("unparseable values should yield 0, got ShortFloat=%v ShortRatio=%v", got.ShortFloat, got.ShortRatio)
	}
}

func TestFetchInstitutionalData_HTTPError(t *testing.T) {
	tr := newMockTransport().on("finviz.com", 403, "forbidden", "text/html")
	e := &Enricher{httpClient: newMockClient(tr)}
	_, err := e.fetchInstitutionalData("AAPL")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 error, got %v", err)
	}
}
