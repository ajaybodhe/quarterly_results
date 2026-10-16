package report

import (
	"fmt"
	"io"
	"strings"

	"quarterly_results/internal/domain"
)

// WriteLLMRating renders the LLM rating block below the deterministic Rating
// block in a stock card. Kept format-similar so the eye can compare them.
func WriteLLMRating(w io.Writer, r *domain.LLMRating) {
	if r == nil {
		return
	}
	mark := "•"
	switch r.Rating.Label {
	case "Positive":
		mark = "▲"
	case "Negative":
		mark = "▼"
	}
	source := r.Metadata.Model
	if len(r.Metadata.Agents) > 0 {
		source += " · " + strings.Join(r.Metadata.Agents, ",")
	}
	fmt.Fprintf(w, "LLM      %s %-9s  Score %+6.1f  Confidence %.0f%%  (%s)\n",
		mark, r.Rating.Label, r.Rating.Score, r.Rating.Confidence*100, source)
	for _, reason := range r.Rating.TopReasons {
		fmt.Fprintf(w, "         %s\n", reason)
	}
}
