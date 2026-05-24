//go:build cabi_dlopen && cabi_panic_inject

package main

// init appends the cabi_panic_inject tag to dlopenBuildTags (declared in
// buildtags_dlopen.go, which is compiled whenever cabi_dlopen is active).
// TestMain in dlopen_test.go then forwards this tag to the .so build so that
// triggerInjectedPanicIfRequested fires inside the .so during panic-injection
// tests.
func init() { dlopenBuildTags = append(dlopenBuildTags, "cabi_panic_inject") }
