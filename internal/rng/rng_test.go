package rng

import (
	"math"
	"testing"
)

func TestStreamsAreReproducibleAndIndependent(t *testing.T) {
	a, b := Stream(42, "shopper", 7), Stream(42, "shopper", 7)
	for range 100 {
		if a.Uint64() != b.Uint64() {
			t.Fatal("the same seed, name, and index gave different numbers")
		}
	}
	if Stream(42, "shopper", 7).Uint64() == Stream(42, "shopper", 8).Uint64() {
		t.Fatal("neighbouring indexes share a stream")
	}
	if Stream(42, "shopper", 7).Uint64() == Stream(43, "shopper", 7).Uint64() {
		t.Fatal("neighbouring seeds share a stream")
	}
}

func TestZipfConcentratesOnLowRanks(t *testing.T) {
	r := Stream(1, "zipf", 0)
	z := NewZipf(1000, 1.1)
	top := 0
	for range 20000 {
		if z.Draw(r) < 10 {
			top++
		}
	}
	if share := float64(top) / 20000; share < 0.35 {
		t.Fatalf("top 10 of 1000 ranks drew %.2f, want a concentrated distribution", share)
	}
	u := NewZipf(1000, 0)
	top = 0
	for range 20000 {
		if u.Draw(r) < 10 {
			top++
		}
	}
	if share := float64(top) / 20000; math.Abs(share-0.01) > 0.005 {
		t.Fatalf("exponent 0 should be uniform, top 10 drew %.3f", share)
	}
}

func TestGeometricMean(t *testing.T) {
	r := Stream(3, "geo", 0)
	sum := 0
	for range 50000 {
		sum += Geometric(r, 5)
	}
	if mean := float64(sum) / 50000; math.Abs(mean-5) > 0.15 {
		t.Fatalf("mean %.2f, want 5", mean)
	}
}
