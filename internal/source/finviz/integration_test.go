//go:build integration

package finviz

import (
	"net/http"
	"testing"
	"time"
)

// TestIntegration_FinvizInstitutional verifies that Finviz returns plausible
// institutional ownership data for MSFT.
func TestIntegration_FinvizInstitutional(t *testing.T) {
	inst, err := FetchInstitutionalData(&http.Client{Timeout: 30 * time.Second}, "MSFT")
	if err != nil {
		t.Fatalf("fetchInstitutionalData(MSFT): %v", err)
	}
	if inst.InstOwn < 50 || inst.InstOwn > 100 {
		t.Errorf("MSFT InstOwn = %.1f%%, want 50–100%%", inst.InstOwn)
	}
	if inst.Activity == "" {
		t.Error("Activity should not be empty")
	}
}
