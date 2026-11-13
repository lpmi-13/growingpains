// Package rng derives independent, reproducible random streams from one seed.
//
// Every stream is named, and optionally indexed, so that concurrency never reorders random
// choices: session 1234 of a run draws the same numbers whatever else is happening.
package rng

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
)

// splitmix64 is the standard seed scrambler for 64-bit generators.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func nameHash(name string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return h.Sum64()
}

// Stream returns the generator for one named stream and index under a seed.
func Stream(seed uint64, name string, index uint64) *rand.Rand {
	a := splitmix64(seed ^ nameHash(name))
	b := splitmix64(a ^ splitmix64(index+1))
	return rand.New(rand.NewPCG(a, b))
}

// Uniform returns a float in [lo, hi).
func Uniform(r *rand.Rand, lo, hi float64) float64 {
	return lo + r.Float64()*(hi-lo)
}

// Exp returns an exponential variate with the given mean.
func Exp(r *rand.Rand, mean float64) float64 {
	return r.ExpFloat64() * mean
}

// Geometric returns a count >= 1 with the given mean.
func Geometric(r *rand.Rand, mean float64) int {
	if mean <= 1 {
		return 1
	}
	p := 1 / mean
	return 1 + int(math.Floor(math.Log(1-r.Float64())/math.Log(1-p)))
}

// LogNormalInt returns a positive integer from a log-normal distribution around median.
func LogNormalInt(r *rand.Rand, median, sigma float64, lo, hi int) int {
	v := int(math.Round(median * math.Exp(r.NormFloat64()*sigma)))
	return max(lo, min(hi, v))
}

// Weighted picks an index from non-negative weights.
func Weighted(r *rand.Rand, weights []float64) int {
	total := 0.0
	for _, w := range weights {
		total += w
	}
	if total <= 0 {
		return 0
	}
	x := r.Float64() * total
	for i, w := range weights {
		if x < w {
			return i
		}
		x -= w
	}
	return len(weights) - 1
}

// Zipf draws ranks in [0, n) with P(k) proportional to 1/(k+1)^s, using a precomputed table.
type Zipf struct {
	cdf []float64
}

// NewZipf builds the table for n ranks and exponent s. s = 0 is uniform.
func NewZipf(n int, s float64) *Zipf {
	cdf := make([]float64, n)
	total := 0.0
	for k := range n {
		total += 1 / math.Pow(float64(k+1), s)
		cdf[k] = total
	}
	for k := range cdf {
		cdf[k] /= total
	}
	return &Zipf{cdf: cdf}
}

// Draw returns a rank.
func (z *Zipf) Draw(r *rand.Rand) int {
	x := r.Float64()
	lo, hi := 0, len(z.cdf)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if z.cdf[mid] < x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
