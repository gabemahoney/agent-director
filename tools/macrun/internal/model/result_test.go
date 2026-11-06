package model_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

func TestCapOutput(t *testing.T) {
	t.Parallel()
	tail := strings.Repeat("a", model.MaxCaseOutputBytes-1)
	for _, tc := range []struct {
		name, in, want string
		cut            bool
	}{
		{name: "at the cap", in: "b" + tail, want: "b" + tail},
		{name: "over the cap", in: "cb" + tail, want: "b" + tail, cut: true},
		// é is two bytes, so the cap's first byte falls inside it.
		{name: "cap inside a character", in: "é" + tail, want: tail, cut: true},
	} {
		got, cut := model.CapOutput(tc.in)
		if got != tc.want || cut != tc.cut {
			t.Errorf("%s: CapOutput kept %d bytes starting %q, cut %v; want %d bytes starting %q, cut %v",
				tc.name, len(got), got[:2], cut, len(tc.want), tc.want[:2], tc.cut)
		}
	}
}

func TestMergeCase(t *testing.T) {
	t.Parallel()
	c := func(r model.Result, elapsed float64, output string, cut bool) model.CaseRecord {
		var reason model.Reason
		if r == model.ResultFail {
			reason = model.ReasonFailed
		}
		return model.CaseRecord{
			CaseResult: model.CaseResult{Case: "go:pkg/a:TestA", Result: r, Reason: reason, Elapsed: elapsed},
			Output:     output,
			OutputCut:  cut,
		}
	}
	for _, tc := range []struct {
		name                string
		first, second, want model.CaseRecord
	}{
		{
			name:   "fail beats pass",
			first:  c(model.ResultPass, 0.25, "in-package\n", false),
			second: c(model.ResultFail, 0.5, "external\n", false),
			want:   c(model.ResultFail, 0.75, "in-package\nexternal\n", false),
		},
		{
			name:   "skip beats pass",
			first:  c(model.ResultSkip, 0, "", true),
			second: c(model.ResultPass, 1, "x", false),
			want:   c(model.ResultSkip, 1, "x", true),
		},
		{
			name:   "fail beats skip",
			first:  c(model.ResultFail, 1, "", false),
			second: c(model.ResultSkip, 1, "", false),
			want:   c(model.ResultFail, 2, "", false),
		},
		{
			// The judging counts an unknown result as no result, so a merge
			// must not hide it behind a pass or skip.
			name:   "an unknown result beats skip",
			first:  c(model.ResultSkip, 1, "", false),
			second: c("crashed", 1, "", false),
			want:   c("crashed", 2, "", false),
		},
	} {
		if got := model.MergeCase(tc.first, tc.second); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: MergeCase = %+v; want %+v", tc.name, got, tc.want)
		}
	}
}
