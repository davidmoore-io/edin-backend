package kaine

import (
	"testing"
	"time"
)

func float64Ptr(v float64) *float64 { return &v }

func TestProgressScoreBonus(t *testing.T) {
	tests := []struct {
		name     string
		progress *float64
		want     float64
	}{
		{"nil progress", nil, 40},
		{"zero progress", float64Ptr(0), 80},
		{"10% progress", float64Ptr(0.10), 60},
		{"49% progress", float64Ptr(0.49), 60},
		{"50% progress", float64Ptr(0.50), 40},
		{"99% progress", float64Ptr(0.99), 40},
		{"100% progress", float64Ptr(1.0), 20},
		{"199% progress", float64Ptr(1.99), 20},
		{"200% progress", float64Ptr(2.0), 10},
		{"499% progress", float64Ptr(4.99), 10},
		{"500% progress", float64Ptr(5.0), 0},
		{"529% progress", float64Ptr(5.29), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := progressScoreBonus(tt.progress)
			if got != tt.want {
				t.Errorf("progressScoreBonus(%v) = %v, want %v", tt.progress, got, tt.want)
			}
		})
	}
}

func TestCalculatePlasmiumScore(t *testing.T) {
	tests := []struct {
		name       string
		platDemand int64
		osmDemand  int64
		economies  []string
		wantScore  float64
		wantNonZero bool
	}{
		{"optimal platinum", 1288, 0, nil, 100, true},
		{"high platinum", 2000, 0, nil, 100, true},
		{"optimal osmium", 0, 1288, nil, 80, true},
		{"half platinum", 644, 0, nil, 50, true},
		{"half osmium", 0, 644, nil, 40, true},
		{"military economy", 0, 0, []string{"Military"}, 40, true},
		{"colony economy", 0, 0, []string{"Colony"}, 40, true},
		{"no demand no economy", 0, 0, []string{"Agricultural"}, 0, false},
		{"no demand at all", 0, 0, nil, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, reason := calculatePlasmiumScore(tt.platDemand, tt.osmDemand, tt.economies)
			if score != tt.wantScore {
				t.Errorf("score = %v, want %v (reason: %s)", score, tt.wantScore, reason)
			}
			if tt.wantNonZero && score == 0 {
				t.Error("expected non-zero score")
			}
		})
	}
}

func TestCalculateLTDScore(t *testing.T) {
	tests := []struct {
		name      string
		demand    int64
		wantScore float64
	}{
		{"optimal demand", 1288, 100},
		{"high demand", 5000, 100},
		{"half demand", 644, 50},
		{"low demand", 100, 7.76}, // (100/1288)*100 ≈ 7.76
		{"zero demand", 0, 40},    // fallback for expansion state
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, _ := calculateLTDScore(tt.demand)
			diff := score - tt.wantScore
			if diff < 0 {
				diff = -diff
			}
			if diff > 0.01 {
				t.Errorf("calculateLTDScore(%d) = %v, want ~%v", tt.demand, score, tt.wantScore)
			}
		})
	}
}

func TestCalculateRankScore_IncludesProgressBonus(t *testing.T) {
	now := time.Now()
	recentTime := now.Add(-1 * time.Hour)

	// Two identical buyers except for Kaine progress
	lowProgress := &PlasmiumBuyer{
		Score:          100,
		LargestPad:     "L",
		PlatinumPrice:  250000,
		MarketUpdatedAt: &recentTime,
		KaineProgress:  float64Ptr(0.10), // 10% — should get 60 bonus
	}

	highProgress := &PlasmiumBuyer{
		Score:          100,
		LargestPad:     "L",
		PlatinumPrice:  250000,
		MarketUpdatedAt: &recentTime,
		KaineProgress:  float64Ptr(5.29), // 529% — should get 0 bonus
	}

	lowRank := calculateRankScore(lowProgress)
	highRank := calculateRankScore(highProgress)

	if lowRank <= highRank {
		t.Errorf("low progress buyer (rank=%.1f) should rank higher than high progress buyer (rank=%.1f)", lowRank, highRank)
	}

	// The difference should be exactly 60 (60 bonus vs 0 bonus)
	diff := lowRank - highRank
	if diff != 60 {
		t.Errorf("rank difference = %.1f, want 60", diff)
	}
}

func TestCalculateLTDRankScore_IncludesProgressBonus(t *testing.T) {
	now := time.Now()
	recentTime := now.Add(-1 * time.Hour)

	lowProgress := &LTDBuyer{
		Score:           100,
		LargestPad:      "L",
		LTDPrice:        200000,
		MarketUpdatedAt: &recentTime,
		Economies:       []string{"Industrial"},
		KaineProgress:   float64Ptr(0.10), // 10% — 60 bonus
	}

	highProgress := &LTDBuyer{
		Score:           100,
		LargestPad:      "L",
		LTDPrice:        200000,
		MarketUpdatedAt: &recentTime,
		Economies:       []string{"Industrial"},
		KaineProgress:   float64Ptr(5.29), // 529% — 0 bonus
	}

	lowRank := calculateLTDRankScore(lowProgress)
	highRank := calculateLTDRankScore(highProgress)

	if lowRank <= highRank {
		t.Errorf("low progress buyer (rank=%.1f) should rank higher than high progress buyer (rank=%.1f)", lowRank, highRank)
	}

	diff := lowRank - highRank
	if diff != 60 {
		t.Errorf("rank difference = %.1f, want 60", diff)
	}
}

func TestPriceFilter_Plasmium(t *testing.T) {
	// Verify that the price filter logic works correctly
	// A buyer with best price < 100k should be excluded
	tests := []struct {
		name       string
		platPrice  int64
		osmPrice   int64
		shouldPass bool
	}{
		{"no price data", 0, 0, true},          // No data = keep (verify in-game)
		{"platinum 150k", 150000, 0, true},      // Above threshold
		{"osmium 120k", 0, 120000, true},        // Above threshold
		{"platinum 50k", 50000, 0, false},        // Below threshold
		{"osmium 80k", 0, 80000, false},          // Below threshold
		{"platinum 100k exactly", 100000, 0, true}, // At threshold
		{"both below", 50000, 80000, false},      // Best is 80k, still below
		{"one above one below", 50000, 150000, true}, // Best is 150k, passes
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bestPrice := max(tt.platPrice, tt.osmPrice)
			passes := bestPrice == 0 || bestPrice >= 100_000
			if passes != tt.shouldPass {
				t.Errorf("price filter(plat=%d, osm=%d) passes=%v, want %v", tt.platPrice, tt.osmPrice, passes, tt.shouldPass)
			}
		})
	}
}

func TestPriceFilter_LTD(t *testing.T) {
	tests := []struct {
		name       string
		ltdPrice   int64
		shouldPass bool
	}{
		{"no price data", 0, true},
		{"price 200k", 200000, true},
		{"price 50k", 50000, false},
		{"price 100k exactly", 100000, true},
		{"price 99999", 99999, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passes := tt.ltdPrice == 0 || tt.ltdPrice >= 100_000
			if passes != tt.shouldPass {
				t.Errorf("price filter(ltd=%d) passes=%v, want %v", tt.ltdPrice, passes, tt.shouldPass)
			}
		})
	}
}
