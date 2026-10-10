package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/spawn"
)

// decimalIntValue implements flag.Value for an int flag read as a base-10
// integer only (strconv.ParseInt(s, 10, 0)): the integers MCP's JSON and the
// TypeScript client's String(n) can send. flag's IntVar reads base 0, so
// "010" would be 8 and "0x10", "0o7", "0b1" and "1_000" would be accepted;
// here "010" is 10 and the others fail fs.Parse, which every verb reports as
// ErrInvalidFlags (b.c4n). A leading + fails too ("+5"), as neither JSON nor
// String(n) writes one; a leading - parses, and the verb refuses a negative
// value where one is not allowed. Every int flag of this package registers
// with fs.Var(newDecimalInt(...), ...), never a flag integer helper.
type decimalIntValue int

// newDecimalInt sets *dst to def, the flag's default, and returns the
// flag.Value that stores the flag's value in *dst.
func newDecimalInt(dst *int, def int) *decimalIntValue {
	*dst = def
	return (*decimalIntValue)(dst)
}

func (d *decimalIntValue) String() string {
	if d == nil {
		return "0"
	}
	return strconv.Itoa(int(*d))
}

func (d *decimalIntValue) Set(s string) error {
	if strings.HasPrefix(s, "+") {
		return errors.New("leading + not allowed")
	}
	n, err := strconv.ParseInt(s, 10, 0)
	if errors.Is(err, strconv.ErrRange) {
		return errors.New("value out of range")
	}
	if err != nil {
		return errors.New("not a base-10 integer")
	}
	*d = decimalIntValue(n)
	return nil
}

// stringSliceValue implements flag.Value for repeated --flag X / --flag X
// arguments. The collected entries land on a backing []string the caller
// supplies via newStringSlice.
type stringSliceValue struct {
	dst *[]string
}

func newStringSlice(dst *[]string) *stringSliceValue { return &stringSliceValue{dst: dst} }

func (s *stringSliceValue) String() string {
	if s == nil || s.dst == nil {
		return ""
	}
	return strings.Join(*s.dst, ",")
}

func (s *stringSliceValue) Set(v string) error {
	*s.dst = append(*s.dst, v)
	return nil
}

// kvSliceValue implements flag.Value for repeated --flag KEY=VALUE
// arguments. Each Set call splits on the first `=`; missing `=` is a hard
// error so a CLI typo surfaces at parse time, not at dispatch.
type kvSliceValue struct {
	dst  *map[string]string
	flag string // for error messages
}

func newKVSlice(dst *map[string]string, flag string) *kvSliceValue {
	return &kvSliceValue{dst: dst, flag: flag}
}

func (k *kvSliceValue) String() string {
	if k == nil || k.dst == nil || *k.dst == nil {
		return ""
	}
	var b strings.Builder
	first := true
	for kk, vv := range *k.dst {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(kk)
		b.WriteByte('=')
		b.WriteString(vv)
	}
	return b.String()
}

func (k *kvSliceValue) Set(v string) error {
	i := strings.IndexByte(v, '=')
	if i <= 0 {
		return fmt.Errorf("%s expects KEY=VALUE, got %q", k.flag, v)
	}
	if *k.dst == nil {
		*k.dst = map[string]string{}
	}
	(*k.dst)[v[:i]] = v[i+1:]
	return nil
}

// buildPermissions assembles a *spawn.Permissions from the three repeated
// flag slices. Returns nil when every slice is empty so the caller can
// leave the field unset.
func buildPermissions(allow, deny, ask []string) *spawn.Permissions {
	if len(allow) == 0 && len(deny) == 0 && len(ask) == 0 {
		return nil
	}
	return &spawn.Permissions{Allow: allow, Deny: deny, Ask: ask}
}
