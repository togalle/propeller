package main

//go:wasmexport naive_fib
func naive_fib(n uint32) uint32 {
	if n <= 1 {
		return n
	}
	return naive_fib(n-1) + naive_fib(n-2)
}

// main is required for the `wasi` target, even if it isn't used.
// See https://wazero.io/languages/tinygo/#why-do-i-have-to-define-main
func main() {}
