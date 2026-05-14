package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---- formatBytes ----

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{int64(1.5 * float64(1024*1024)), "1.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{4_509_715_660, "4.2 GB"}, // 4.2 * 1024^3, truncated
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, formatBytes(tc.in))
		})
	}
}

// ---- formatDuration ----

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "< 1 sec"},
		{-time.Second, "< 1 sec"},
		{time.Second, "1 sec"},
		{42 * time.Second, "42 sec"},
		{90 * time.Second, "1 min 30 sec"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1 h 2 min 3 sec"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, formatDuration(tc.in))
		})
	}
}

// ---- fitModel ----

func TestFitModel_TwoDistinctPoints(t *testing.T) {
	// 1 MB in 0.5 s, 10 MB in 1.4 s → beta = 0.9/(9 MiB), alpha ≈ 0.4 s
	s1, s2 := int64(1<<20), int64(10<<20)
	alpha, beta := fitModel(s1, s2, 0.5, 1.4)
	assert.InDelta(t, 0.4, alpha, 0.02)
	assert.Positive(t, beta)
}

func TestFitModel_NegativeAlphaClamped(t *testing.T) {
	// Noisy: second file is only marginally slower → would produce negative alpha
	s1, s2 := int64(1<<20), int64(2<<20)
	alpha, _ := fitModel(s1, s2, 1.0, 1.0)
	assert.GreaterOrEqual(t, alpha, 0.0)
}

func TestFitModel_NegativeBetaClamped(t *testing.T) {
	// Second file was faster despite being larger (network jitter) → beta clamped to 0
	s1, s2 := int64(1<<20), int64(10<<20)
	_, beta := fitModel(s1, s2, 1.0, 0.5)
	assert.GreaterOrEqual(t, beta, 0.0)
}

func TestFitModel_SameSize(t *testing.T) {
	// ds = 0 → average of the two times, no overhead term
	alpha, beta := fitModel(1<<20, 1<<20, 0.5, 0.7)
	assert.Equal(t, 0.0, alpha)
	assert.Positive(t, beta)
}
