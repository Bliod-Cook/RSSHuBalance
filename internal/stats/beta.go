package stats

import (
	"math"
	"math/rand/v2"
)

func sampleBeta(rng *rand.Rand, a, b float64) float64 {
	x := sampleGamma(rng, a)
	return x / (x + sampleGamma(rng, b))
}

// sampleGamma draws from Gamma(shape, 1) with the Marsaglia–Tsang method.
func sampleGamma(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		return sampleGamma(rng, shape+1) * math.Pow(rng.Float64(), 1/shape)
	}
	d := shape - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
