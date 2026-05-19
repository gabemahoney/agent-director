package main

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/claude-director/internal/spawn"
)

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
