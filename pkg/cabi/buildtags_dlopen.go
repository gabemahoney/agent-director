//go:build cabi_dlopen

package main

// dlopenBuildTags accumulates the set of build tags active in the test
// binary. TestMain reads this slice to forward matching tags to the
// go build -buildmode=c-shared invocation, so the .so reflects the same
// tag set as the test binary — minus cabi_dlopen itself, which gates the
// dlopen test-harness files (dlopen_helper.go) that must not be compiled
// into the production .so artifact.
var dlopenBuildTags []string

func init() { dlopenBuildTags = append(dlopenBuildTags, "cabi_dlopen") }
