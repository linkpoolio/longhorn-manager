package controller

import (
	"testing"
	"time"
)

func TestCappedControllerRateLimiterCapsBackoff(t *testing.T) {
	rl := CappedControllerRateLimiter(time.Minute)
	item := "engine-x"
	var last time.Duration
	for i := 0; i < 40; i++ {
		last = rl.When(item)
	}
	if last > time.Minute {
		t.Errorf("backoff after 40 failures = %v, expected cap at 1m", last)
	}
	if last < 30*time.Second {
		t.Errorf("backoff after 40 failures = %v, expected to have grown to the cap", last)
	}
}

func TestEnhancedDefaultRateLimiterUncappedReference(t *testing.T) {
	// Documents why the capped variant exists: the default parks items for
	// far longer than an attach ladder can tolerate.
	rl := EnhancedDefaultControllerRateLimiter()
	item := "engine-x"
	var last time.Duration
	for i := 0; i < 40; i++ {
		last = rl.When(item)
	}
	if last < 10*time.Minute {
		t.Errorf("default backoff after 40 failures = %v, expected >10m (else the capped variant is redundant)", last)
	}
}
