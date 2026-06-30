package handlers

import (
	"testing"
	"time"

	"bakeu/internal/repository"
)

func tashkent() *time.Location { return time.FixedZone("Asia/Tashkent", tashkentOffset) }

func TestBucketsToChartBackfillsSevenDays(t *testing.T) {
	loc := tashkent()
	// A Monday in local time.
	weekStart := time.Date(2026, time.June, 22, 0, 0, 0, 0, loc)

	// Delivered revenue on day 0 (Mon) and day 3 (Thu); other days absent.
	buckets := []repository.DayBucket{
		{Day: weekStart.UTC(), Value: 100},
		{Day: weekStart.AddDate(0, 0, 3).UTC(), Value: 250},
	}

	chart := bucketsToChart(buckets, weekStart, loc)

	if len(chart) != 7 {
		t.Fatalf("expected exactly 7 points, got %d", len(chart))
	}
	want := []chartPoint{
		{"Mon", 100}, {"Tue", 0}, {"Wed", 0}, {"Thu", 250},
		{"Fri", 0}, {"Sat", 0}, {"Sun", 0},
	}
	for i, w := range want {
		if chart[i] != w {
			t.Errorf("point %d = %+v, want %+v", i, chart[i], w)
		}
	}

	var sum float64
	for _, p := range chart {
		sum += p.Value
	}
	if sum != 350 {
		t.Errorf("chart total = %v, want 350", sum)
	}
}

func TestBucketsToChartEmpty(t *testing.T) {
	loc := tashkent()
	weekStart := time.Date(2026, time.June, 22, 0, 0, 0, 0, loc)

	chart := bucketsToChart(nil, weekStart, loc)
	if len(chart) != 7 {
		t.Fatalf("expected 7 points for empty input, got %d", len(chart))
	}
	for i, p := range chart {
		if p.Value != 0 {
			t.Errorf("point %d value = %v, want 0", i, p.Value)
		}
	}
}

func TestPercentChange(t *testing.T) {
	if got := percentChange(150, 0); got != nil {
		t.Errorf("prior 0 should yield nil, got %v", *got)
	}
	if got := percentChange(0, 0); got != nil {
		t.Errorf("both 0 should yield nil, got %v", *got)
	}
	if got := percentChange(150, 100); got == nil || *got != 50 {
		t.Errorf("(150 vs 100) = %v, want 50", got)
	}
	if got := percentChange(100, 200); got == nil || *got != -50 {
		t.Errorf("(100 vs 200) = %v, want -50", got)
	}
}

func TestParseMonth(t *testing.T) {
	now := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)

	cases := map[string]time.Month{
		"":         time.June, // defaults to current month
		"February": time.February,
		"february": time.February,
		"Feb":      time.February,
		"2":        time.February,
		"12":       time.December,
	}
	for in, want := range cases {
		got, err := parseMonth(in, now)
		if err != nil {
			t.Errorf("parseMonth(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseMonth(%q) = %v, want %v", in, got, want)
		}
	}

	for _, bad := range []string{"0", "13", "Smarch", "-1"} {
		if _, err := parseMonth(bad, now); err == nil {
			t.Errorf("parseMonth(%q) expected error, got nil", bad)
		}
	}
}
