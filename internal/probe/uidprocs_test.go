package probe

import (
	"errors"
	"fmt"
	"reflect"
	"syscall"
	"testing"
)

func TestDarwinUIDProcessReader(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2*kinfoProcSize)
	for i, pid := range []int32{4242, 99} {
		plantEntry(buf, i*kinfoProcSize, 1, darwinStartSec, darwinStartUsec)
		plantStat(buf, i*kinfoProcSize, kinfoStatSRUN)
		plantPID(buf, i*kinfoProcSize, pid)
	}
	fetch, check := fakeKinfo(t, 501, buf, nil)
	got, err := (darwinUIDProcessReader{fetchKinfoUID: fetch}).PIDs(501)
	check()
	if err != nil || !reflect.DeepEqual(got, []int{4242, 99}) {
		t.Fatalf("selection = %v, %v", got, err)
	}
	for _, tc := range []struct {
		name        string
		failures    int
		err         error
		data        []byte
		calls       int
		wantErr     error
		wantFailure bool
	}{
		{"retry-then-success", 3, fmt.Errorf("growing buffer: %w", syscall.ENOMEM), buf, 4, nil, false},
		{"retry-limit", 4, syscall.ENOMEM, buf, 4, syscall.ENOMEM, true},
		{"other-error", 1, syscall.EACCES, buf, 1, syscall.EACCES, true},
		{"drift", 0, nil, buf[:len(buf)-1], 1, ErrKinfoLayoutDrift, true},
		{"empty", 0, nil, nil, 1, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []int
			r := darwinUIDProcessReader{fetchKinfoUID: func(uid int) ([]byte, error) {
				calls = append(calls, uid)
				if len(calls) <= tc.failures {
					return nil, tc.err
				}
				return tc.data, nil
			}}
			got, err := r.PIDs(501)
			wantCalls := make([]int, tc.calls)
			for i := range wantCalls {
				wantCalls[i] = 501
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("uid fetches = %v; want %v", calls, wantCalls)
			}
			if tc.wantFailure {
				if err == nil || got != nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
					t.Fatalf("selection = %v, %v; want nil, %v", got, err, tc.wantErr)
				}
			} else if err != nil || !reflect.DeepEqual(got, []int{4242, 99}) {
				t.Fatalf("selection = %v, %v", got, err)
			}
		})
	}
}
