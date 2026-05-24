package main

// fuzz_verbs_test.go provides one FuzzAd* target per callable manifest verb
// (15 verbs) plus FuzzAdOpen and FuzzAdClose for the lifecycle exports = 17
// targets total.
//
// No build tag: seed-corpus runs are part of the default test suite.
// Full fuzzing requires an explicit -fuzz flag, e.g.:
//
//	go test -fuzz=FuzzAdSpawn ./pkg/cabi/...
//
// Design:
//   - Handle-requiring verbs (all except ad_version) call runVerb() directly
//     with a stub fn that returns a sentinel error. runVerb's handle-resolution
//     code returns ErrUnknownHandle before calling fn for any input that lacks
//     a valid registered handle, which is always the case here. This validates
//     the full JSON-parsing + envelope-shaping path without needing a live store.
//   - ad_version (handle-free) calls runVerb() with the real Version() fn
//     so that both malformed and well-formed inputs exercise the success path.
//   - ad_open and ad_close are NOT dispatched through runVerb (they have their
//     own inline recover() in lifecycle.go), so they use dedicated in-process
//     helpers defined below.

import (
	"encoding/json"
	"fmt"
	"testing"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// ── in-process helpers for lifecycle exports ──────────────────────────────────

// runAdOpenInProc mirrors the inner recover/parse/open logic of ad_open in
// lifecycle.go, without the *C.char I/O boundary.
func runAdOpenInProc(params []byte) (result []byte) {
	defer func() {
		if r := recover(); r != nil {
			result = errorEnvelope("ErrInternal", "internal error")
		}
	}()
	triggerInjectedPanicIfRequested(params)
	var p openParams
	if err := json.Unmarshal(params, &p); err != nil {
		result = errorEnvelope("ErrInternal", "ad_open: invalid JSON params")
		return
	}
	client, err := pkgapi.New(pkgapi.Options{
		StorePath:       p.StorePath,
		ConfigPath:      p.ConfigPath,
		TmuxCommand:     p.TmuxCommand,
		CreateIfMissing: p.CreateIfMissing,
	})
	if err != nil {
		result = classifyAndEnvelope(err)
		return
	}
	handle := registry.mint(client)
	result = successEnvelope(map[string]string{"handle": handle})
	return
}

// runAdCloseInProc mirrors the inner recover/parse/delete logic of ad_close in
// lifecycle.go, without the *C.char I/O boundary. The caller is not closed —
// fuzz tests are not responsible for resource cleanup.
func runAdCloseInProc(params []byte) (result []byte) {
	defer func() {
		if r := recover(); r != nil {
			result = errorEnvelope("ErrInternal", "internal error")
		}
	}()
	triggerInjectedPanicIfRequested(params)
	var p closeParams
	if err := json.Unmarshal(params, &p); err != nil {
		result = errorEnvelope("ErrInternal", "ad_close: invalid JSON params")
		return
	}
	registry.delete(p.Handle) // unknown handle is a no-op success per spec
	result = successEnvelope(nil)
	return
}

// ── lifecycle fuzz targets ────────────────────────────────────────────────────

func FuzzAdOpen(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdOpen: panic escaped recover: %v", r)
			}
		}()
		assertWellFormedEnvelope(t, runAdOpenInProc([]byte(input)))
	})
}

func FuzzAdClose(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdClose: panic escaped recover: %v", r)
			}
		}()
		assertWellFormedEnvelope(t, runAdCloseInProc([]byte(input)))
	})
}

// ── handle-free verb ──────────────────────────────────────────────────────────

func FuzzAdVersion(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdVersion: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_version", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return pkgapi.Version()
		})
		assertWellFormedEnvelope(t, result)
	})
}

// ── handle-requiring verbs (14) ───────────────────────────────────────────────
//
// For all handle-requiring verbs the fn lambda is never reached: runVerb
// returns ErrUnknownHandle before calling fn when the handle is absent or
// unregistered. The lambda exists only to satisfy the runVerb signature.

func FuzzAdSpawn(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdSpawn: panic escaped recover: %v", r)
			}
		}()
		result := runVerb("ad_spawn", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdStatus(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdStatus: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_status", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdGet(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdGet: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_get", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdList(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdList: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_list", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdSendKeys(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdSendKeys: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_send_keys", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdReadPane(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdReadPane: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_read_pane", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdKill(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdKill: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_kill", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdPause(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdPause: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_pause", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdDecide(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdDecide: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_decide", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdResume(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdResume: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_resume", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdFindMissing(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdFindMissing: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_find_missing", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdExpire(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdExpire: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_expire", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdDelete(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdDelete: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_delete", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}

func FuzzAdMakeTemplate(f *testing.F) {
	for _, s := range sharedFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("FuzzAdMakeTemplate: panic escaped: %v", r)
			}
		}()
		result := runVerb("ad_make_template", []byte(input), func(_ *pkgapi.Client, _ []byte) (any, error) {
			return nil, fmt.Errorf("fuzz: no real store")
		})
		assertWellFormedEnvelope(t, result)
	})
}
