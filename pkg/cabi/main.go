package main

// main is a required stub for buildmode=c-shared. The package exposes its
// functionality exclusively through the exported C functions declared in
// lifecycle.go (ad_open, ad_close, ad_free_cstring) and the per-verb exports
// added in subsequent tasks. This function is never called at runtime when the
// shared library is loaded via dlopen; it exists only to satisfy the Go
// toolchain's requirement that a c-shared build target be a main package.
func main() {}
