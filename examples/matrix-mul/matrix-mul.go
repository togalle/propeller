package main

import "math"

//go:wasmexport matrix_mul
func matrix_mul(n uint32) uint32 {
	// Create two n×n matrices
	a := make([][]float64, n)
	b := make([][]float64, n)
	c := make([][]float64, n)
	
	for i := range n {
		a[i] = make([]float64, n)
		b[i] = make([]float64, n)
		c[i] = make([]float64, n)
		for j := range n {
			a[i][j] = math.Sqrt(float64(i+j)) + 1
			b[i][j] = math.Sqrt(float64(i*j)) + 1
		}
	}

	// Multiply matrices a and b, store in c
	for i := range n {
		for j := range n {
			for k := range n {
				c[i][j] += a[i][k] * b[k][j]
			}
		}
	}

	// Return sum of result matrix
	var result uint32
	for i := range n {
		for j := range n {
			result += uint32(c[i][j]) % 10
		}
	}
	return result
}

// main is required for the `wasi` target, even if it isn't used.
// See https://wazero.io/languages/tinygo/#why-do-i-have-to-define-main
func main() {}
