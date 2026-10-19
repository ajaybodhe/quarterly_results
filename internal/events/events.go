// Package events builds point-in-time earnings events for post-earnings-drift
// research: features known at the reaction-day close, and labels measured
// strictly after it. The same feature code feeds live drift predictions
// (internal/enrich) and the historical export (--export-events), so a model
// evaluated on the export sees exactly what it will see live.
package events

import (
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/indicators"
)

const (
	// Bars before the reaction day that form the "normal volume" baseline.
	volumeLookback = 20
	// Drift horizons, in trading bars after the reaction-day close.
	shortHorizon = 5
	longHorizon  = 20
	// A live drift prediction is only made while the reaction is fresh: the
	// reaction day must be among the last freshBars bars.
	freshBars = 5
)

// Labels are outcomes measured after the decision time (the reaction-day
// close). They are training/evaluation targets only — never features.
type Labels struct {
	Drift5     *float64 `json:"drift5,omitempty"`      // stock return, reaction close → +5 bars
	Drift20    *float64 `json:"drift20,omitempty"`     // stock return, reaction close → +20 bars
	Drift5Adj  *float64 `json:"drift5_adj,omitempty"`  // Drift5 minus the sector ETF over the same dates
	Drift20Adj *float64 `json:"drift20_adj,omitempty"` // Drift20 minus the sector ETF over the same dates
	Drift5Mkt  *float64 `json:"drift5_mkt,omitempty"`  // Drift5 minus SPY over the same dates
	Drift20Mkt *float64 `json:"drift20_mkt,omitempty"` // Drift20 minus SPY over the same dates
	// Auxiliary investor-response targets (the paper's multitask idea).
	AbnormalVolume1to5 *float64 `json:"abnormal_volume_1to5,omitempty"` // mean volume +1…+5 ÷ pre-event mean
	Post7Ret           *float64 `json:"post7_ret,omitempty"`            // reaction close → ~7 calendar days later
	// Placebo: the stock's return over the ~7 days before the announcement.
	// Features computed after the event should not "predict" it better than
	// chance; if they do, look for leakage or pre-announcement information flow.
	Pre7Ret *float64 `json:"pre7_ret,omitempty"`
}

// Features computes drift features for one announcement from price history.
// reactionDay and priorClose come from indicators.ReactionDay (live) or an
// EarningsReaction (history). ok is false when the reaction-day bar is missing.
func Features(announce, reactionDay time.Time, priorClose float64, epsSurprisePct *float64,
	prices, etf []domain.PricePoint, etfName string) (*domain.DriftFeatures, bool) {
	if priorClose == 0 {
		return nil, false
	}
	rxClose, rxOpen, ok := barOn(prices, reactionDay)
	if !ok {
		return nil, false
	}
	f := &domain.DriftFeatures{
		AnnouncementDate: announce.Format("2006-01-02"),
		ReactionDay:      reactionDay.Format("2006-01-02"),
		ReactionRetPct:   indicators.PctChange(priorClose, rxClose),
		EPSSurprisePct:   epsSurprisePct,
		SectorETF:        etfName,
	}
	if rxOpen > 0 {
		f.GapRetPct = indicators.PctChange(priorClose, rxOpen)
	}
	if v, ok := preAnnouncementReturn(prices, announce); ok {
		f.Pre7Ret = &v
	}
	dayBefore := announce.AddDate(0, 0, -1)
	if v, ok := indicators.ReturnBetween(prices, dayBefore.AddDate(0, 0, -30), dayBefore); ok {
		f.Ret1MPre = &v
	}
	if v, ok := indicators.ReturnBetween(prices, dayBefore.AddDate(0, 0, -182), dayBefore); ok {
		f.Ret6MPre = &v
	}
	if v, ok := indicators.AbnormalVolume(prices, reactionDay, 0, 0, volumeLookback); ok {
		f.AbnormalVolume = &v
	}
	if len(etf) > 0 {
		if v, ok := indicators.ReturnBetween(etf, reactionDay.AddDate(0, 0, -30), reactionDay); ok {
			f.SectorRet1M = &v
		}
	}
	return f, true
}

// LiveFeatures computes drift features for a report that was just released:
// it resolves the reaction day from the release timing, and returns ok=false
// unless that day's bar exists and is among the last freshBars bars.
func LiveFeatures(announceDate, earningsTime string, epsSurprisePct *float64,
	prices, etf []domain.PricePoint, etfName string) (*domain.DriftFeatures, bool) {
	announce, err := time.Parse("2006-01-02", announceDate)
	if err != nil || len(prices) == 0 {
		return nil, false
	}
	reactionDay, priorClose, ok := indicators.ReactionDay(announce, earningsTime, prices)
	if !ok {
		return nil, false
	}
	if !Fresh(reactionDay, prices) {
		return nil, false
	}
	f, ok := Features(announce, reactionDay, priorClose, epsSurprisePct, prices, etf, etfName)
	if ok {
		f.ReleaseTiming = earningsTime
	}
	return f, ok
}

// Fresh reports whether the reaction just happened: reactionDay's bar is
// within the last freshBars bars of the series.
func Fresh(reactionDay time.Time, prices []domain.PricePoint) bool {
	n := len(prices)
	if n == 0 || prices[n-1].Date.Before(reactionDay) {
		return false
	}
	first := n - freshBars
	if first < 0 {
		first = 0
	}
	return !prices[first].Date.After(reactionDay)
}

// LabelsAt measures drift and auxiliary outcomes after the reaction-day close:
// raw, sector-adjusted (etf) and market-adjusted (market, e.g. SPY; may be nil).
// Windows that haven't completed yet are left nil.
func LabelsAt(announce, reactionDay time.Time, prices, etf, market []domain.PricePoint) Labels {
	var l Labels
	for _, h := range []struct {
		n             int
		raw, adj, mkt **float64
	}{{shortHorizon, &l.Drift5, &l.Drift5Adj, &l.Drift5Mkt}, {longHorizon, &l.Drift20, &l.Drift20Adj, &l.Drift20Mkt}} {
		ret, end, ok := indicators.ForwardReturn(prices, reactionDay, h.n)
		if !ok {
			continue
		}
		r := ret
		*h.raw = &r
		if bench, ok := indicators.ReturnBetween(etf, reactionDay, end); ok {
			a := ret - bench
			*h.adj = &a
		}
		if bench, ok := indicators.ReturnBetween(market, reactionDay, end); ok {
			m := ret - bench
			*h.mkt = &m
		}
	}
	if v, ok := indicators.AbnormalVolume(prices, reactionDay, 1, shortHorizon, volumeLookback); ok {
		l.AbnormalVolume1to5 = &v
	}
	if rx, _, ok := barOn(prices, reactionDay); ok {
		if later, ok := indicators.ClosestPrice(prices, reactionDay.AddDate(0, 0, 7)); ok && rx != 0 {
			// Only once a bar at least 7 days out exists.
			if last := prices[len(prices)-1].Date; !last.Before(reactionDay.AddDate(0, 0, 7)) {
				v := indicators.PctChange(rx, later)
				l.Post7Ret = &v
			}
		}
	}
	if v, ok := preAnnouncementReturn(prices, announce); ok {
		l.Pre7Ret = &v
	}
	return l
}

// preAnnouncementReturn is the close-to-close return from ~7 calendar days
// before the announcement to the day before it.
func preAnnouncementReturn(prices []domain.PricePoint, announce time.Time) (float64, bool) {
	before, ok1 := indicators.ClosestPrice(prices, announce.AddDate(0, 0, -1))
	start, ok2 := indicators.ClosestPrice(prices, announce.AddDate(0, 0, -8))
	if !ok1 || !ok2 || start == 0 {
		return 0, false
	}
	return indicators.PctChange(start, before), true
}

// barOn returns the close and open of the bar dated exactly on day.
func barOn(prices []domain.PricePoint, day time.Time) (close, open float64, ok bool) {
	want := day.Format("2006-01-02")
	for _, p := range prices {
		if p.Date.Format("2006-01-02") == want {
			return p.Close, p.Open, true
		}
	}
	return 0, 0, false
}
