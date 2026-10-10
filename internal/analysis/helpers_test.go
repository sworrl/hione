package analysis

import (
	"math/cmplx"
	"math/rand"
)

func cmplxAbs(c complex128) float64 { return cmplx.Abs(c) }
func newRand() *rand.Rand           { return rand.New(rand.NewSource(5)) }
