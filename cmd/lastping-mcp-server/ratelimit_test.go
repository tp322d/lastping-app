package main

import (
	"testing"
	"time"
)

func TestTokenRateLimiter_AllowsThenBlocks(t *testing.T) {
	rl := newTokenRateLimiter(16, 3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.Allow("k") {
			t.Fatalf("request %d blocked, want allowed", i+1)
		}
	}
	if rl.Allow("k") {
		t.Fatal("4th request allowed, want blocked")
	}
}

func TestTokenRateLimiter_PerTokenIsolation(t *testing.T) {
	rl := newTokenRateLimiter(16, 1, time.Minute)
	if !rl.Allow("a") {
		t.Fatal("a[1] blocked")
	}
	if rl.Allow("a") {
		t.Fatal("a[2] allowed, want blocked")
	}
	if !rl.Allow("b") {
		t.Fatal("b[1] blocked — token isolation broken")
	}
}

func TestTokenRateLimiter_WindowReset(t *testing.T) {
	rl := newTokenRateLimiter(16, 1, 20*time.Millisecond)
	if !rl.Allow("k") {
		t.Fatal("k[1] blocked")
	}
	if rl.Allow("k") {
		t.Fatal("k[2] allowed before window reset")
	}
	time.Sleep(30 * time.Millisecond)
	if !rl.Allow("k") {
		t.Fatal("k blocked after window reset")
	}
}

func TestHashToken(t *testing.T) {
	if hashToken("x") != hashToken("x") {
		t.Fatal("hashToken not deterministic")
	}
	if hashToken("x") == hashToken("y") {
		t.Fatal("hashToken collided on distinct tokens")
	}
	if got := len(hashToken("x")); got != 16 {
		t.Fatalf("hashToken len = %d, want 16", got)
	}
}
