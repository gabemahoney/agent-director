package clisetup

import "flag"

// isRepeatableFlag reads the accumulation capability from a Value, so a new
// flag using a collecting Value needs no separate exemption by flag name.
func isRepeatableFlag(v flag.Value) bool {
	r, ok := v.(interface{ IsRepeatableFlag() bool })
	return ok && r.IsRepeatableFlag()
}

// TrackSingleFlag wraps a non-repeatable Value after its registration. Use
// fs.VisitAll(TrackSingleFlag) after all registrations, then visit with a
// SingleFlagCheck after a successful Parse, before using any parsed values.
// The original registration, default and usage stay available to flag readers.
func TrackSingleFlag(f *flag.Flag) {
	if _, tracked := f.Value.(*singleFlagValue); tracked || isRepeatableFlag(f.Value) {
		return
	}
	f.Value = &singleFlagValue{value: f.Value}
}

type singleFlagValue struct {
	value flag.Value
	count int
}

func (v *singleFlagValue) String() string {
	if v == nil || v.value == nil {
		return ""
	}
	return v.value.String()
}

func (v *singleFlagValue) IsBoolFlag() bool {
	if v == nil {
		return false
	}
	b, ok := v.value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func (v *singleFlagValue) Set(s string) error {
	v.count++
	// Every copy must reach its original parser so malformed later values
	// retain their real parse error and argv order. These Values populate
	// disposable or handler-local fields; SingleFlagCheck refuses a repeat
	// after a clean parse, before a handler can use those fields.
	return v.value.Set(s)
}

// SingleFlagCheck collects a repeat error after Parse. Its zero value is
// ready to use. Actual Parse errors and help retain their existing handling
// at the caller.
type SingleFlagCheck struct {
	Err error
}

// Visit is an fs.VisitAll callback, keeping FlagSet ownership at the caller.
func (c *SingleFlagCheck) Visit(f *flag.Flag) {
	if v, ok := f.Value.(*singleFlagValue); ok && v.count > 1 && c.Err == nil {
		c.Err = repeatedFlagError(f.Name)
	}
}

// repeatedFlagError takes a registered name without leading dashes and
// deliberately omits caller values, which may contain sensitive data.
func repeatedFlagError(name string) error {
	return &flagRepeatError{name: name}
}

// flagRepeatError distinguishes pending occurrence refusals from malformed
// global values while the two global scans finish validating every copy.
type flagRepeatError struct {
	name   string
	global bool
}

func (e *flagRepeatError) Error() string {
	return "flag --" + e.name + " may only be specified once"
}

// IsGlobalFlagRepeat reports whether err is a pending global repeat. A caller
// must finish scanning later global positions before reporting it, so a later
// malformed global value can still take precedence.
func IsGlobalFlagRepeat(err error) bool {
	e, ok := err.(*flagRepeatError)
	return ok && e.global
}

// GlobalFlagScanError combines a pending global repeat with the next scan.
// Malformed globals win over repeats; otherwise the earlier global repeat
// wins over a later global or verb repeat. pending must be nil or a global
// repeat returned by an earlier scan.
func GlobalFlagScanError(pending, next error) error {
	if next != nil {
		if _, repeat := next.(*flagRepeatError); !repeat {
			return next
		}
	}
	if pending != nil {
		return pending
	}
	return next
}

// ParseError preserves an actual parse error, including help and unknown
// flags, before considering a recorded repeat. It takes no FlagSet, so the
// caller can keep all FlagSet method calls on its own locally built set.
func (c *SingleFlagCheck) ParseError(err error) error {
	if err != nil {
		return err
	}
	return c.Err
}
