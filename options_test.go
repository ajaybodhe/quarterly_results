package main

import (
	"math"
	"testing"
)

func TestClosestStrike(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		got := closestStrike(nil, 100)
		if got.Strike != 0 {
			t.Errorf("expected zero-value, got Strike=%v", got.Strike)
		}
	})

	t.Run("single contract exact match", func(t *testing.T) {
		contracts := []yahooOption{{Strike: 100, Bid: 1, Ask: 2}}
		got := closestStrike(contracts, 100)
		if got.Strike != 100 {
			t.Errorf("got Strike=%v, want 100", got.Strike)
		}
	})

	t.Run("picks closer strike below", func(t *testing.T) {
		// target=102: |100-102|=2, |105-102|=3 → 100 is closer
		contracts := []yahooOption{{Strike: 95}, {Strike: 100}, {Strike: 105}}
		got := closestStrike(contracts, 102)
		if got.Strike != 100 {
			t.Errorf("got Strike=%v, want 100", got.Strike)
		}
	})

	t.Run("picks closer strike above", func(t *testing.T) {
		// target=103: |100-103|=3, |105-103|=2 → 105 is closer
		contracts := []yahooOption{{Strike: 95}, {Strike: 100}, {Strike: 105}}
		got := closestStrike(contracts, 103)
		if got.Strike != 105 {
			t.Errorf("got Strike=%v, want 105", got.Strike)
		}
	})
}

func TestContractMid(t *testing.T) {
	cases := []struct {
		c    yahooOption
		want float64
	}{
		{yahooOption{Bid: 1.0, Ask: 3.0, LastPrice: 9.9}, 2.0},  // uses mid
		{yahooOption{Bid: 0.0, Ask: 3.0, LastPrice: 2.5}, 2.5},  // bid=0, fallback
		{yahooOption{Bid: 1.0, Ask: 0.0, LastPrice: 2.5}, 2.5},  // ask=0, fallback
		{yahooOption{Bid: 0.0, Ask: 0.0, LastPrice: 2.5}, 2.5},  // both zero, fallback
		{yahooOption{Bid: 2.0, Ask: 4.0, LastPrice: 0.0}, 3.0},  // normal mid, lastPrice irrelevant
	}
	for _, tc := range cases {
		got := contractMid(tc.c)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("contractMid(%+v) = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func TestCleanIV(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{0.84, 0.84},
		{0.001, 0.001},
		{19.99, 19.99},
		{0.0, 0},           // exactly 0 → invalid
		{-0.5, 0},          // negative → invalid
		{20.0, 20.0},       // exactly 20 is valid (condition is iv > 20, not >=)
		{20.001, 0},        // just above 20 → invalid
		{math.Inf(1), 0},   // +Inf
		{math.Inf(-1), 0},  // -Inf
		{math.NaN(), 0},    // NaN
	}
	for _, tc := range cases {
		got := cleanIV(tc.input)
		if math.IsNaN(tc.want) {
			if !math.IsNaN(got) {
				t.Errorf("cleanIV(%v) = %v, want NaN", tc.input, got)
			}
		} else if got != tc.want {
			t.Errorf("cleanIV(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestComputeMaxPain(t *testing.T) {
	t.Run("nil slices", func(t *testing.T) {
		got := computeMaxPain(nil, nil)
		if got != 0 {
			t.Errorf("expected 0, got %v", got)
		}
	})

	t.Run("empty slices", func(t *testing.T) {
		got := computeMaxPain([]yahooOption{}, []yahooOption{})
		if got != 0 {
			t.Errorf("expected 0, got %v", got)
		}
	})

	t.Run("deterministic case", func(t *testing.T) {
		// At K=90:  call_pain=0,                put_pain = 50*(100-90) + 100*(90-90) = 500  → total=500
		// At K=100: call_pain=0,                put_pain = 0                                 → total=0
		// At K=110: call_pain = 100*(110-100),  put_pain = 0                                 → total=1000
		// Min pain = 0 at K=100
		calls := []yahooOption{
			{Strike: 100, OpenInterest: 100},
			{Strike: 110, OpenInterest: 50},
		}
		puts := []yahooOption{
			{Strike: 100, OpenInterest: 50},
			{Strike: 90, OpenInterest: 100},
		}
		got := computeMaxPain(calls, puts)
		if got != 100 {
			t.Errorf("computeMaxPain() = %v, want 100", got)
		}
	})

	t.Run("single strike", func(t *testing.T) {
		// Only one candidate strike → must return it
		calls := []yahooOption{{Strike: 150, OpenInterest: 10}}
		puts := []yahooOption{{Strike: 150, OpenInterest: 10}}
		got := computeMaxPain(calls, puts)
		if got != 150 {
			t.Errorf("computeMaxPain() = %v, want 150", got)
		}
	})
}
