//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

// Admission boundary tests poll journals containing multiple MiB of input.
// Re-decoding every old record on every poll competes with the writer under
// race instrumentation and can expire the unchanged observation deadline on
// an obsolete snapshot. Keep the reader's semantics, decoding only new lines.
type submissionRecordCache struct {
	mu      sync.Mutex
	path    string
	raw     []byte
	records []submissionRecord
}

func (c *submissionRecordCache) decode(path string, raw []byte) ([]submissionRecord, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	previous, records := c.raw, c.records
	if path != c.path || len(raw) < len(previous) || !bytes.Equal(raw[:len(previous)], previous) {
		previous, records = nil, nil
	}
	lines := bytes.Split(raw[len(previous):], []byte("\n"))
	for _, line := range lines[:len(lines)-1] {
		var record submissionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	c.path, c.raw, c.records = path, bytes.Clone(raw), records
	if len(records) == 0 {
		return nil, nil
	}
	result := append([]submissionRecord(nil), records...)
	for i := range result {
		if result[i].Data.InputEpoch != nil {
			epoch := *result[i].Data.InputEpoch
			result[i].Data.InputEpoch = &epoch
		}
	}
	return result, nil
}

func uncachedSubmissionRecords(raw []byte) ([]submissionRecord, error) {
	var records []submissionRecord
	lines := bytes.Split(raw, []byte("\n"))
	for _, line := range lines[:len(lines)-1] {
		var record submissionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func TestSubmissionRecordCachePreservesSnapshots(t *testing.T) {
	first := []byte("{\"kind\":\"submission_accepted\",\"data\":{\"text\":\"café 🐝\",\"input_epoch\":2}}\n")
	second := []byte("{\"kind\":\"response_retired\",\"data\":{\"request_id\":7}}\n")
	var cache submissionRecordCache
	for _, snapshot := range []struct {
		path string
		raw  []byte
	}{
		{"a", nil}, {"a", first}, {"a", first},
		{"a", append(bytes.Clone(first), second[:len(second)-1]...)},
		{"a", append(bytes.Clone(first), second...)},
		{"a", bytes.ReplaceAll(first, []byte("café"), []byte("cafe"))},
		{"a", first}, {"b", append(bytes.Clone(first), second...)},
		{"b", []byte("{bad}\n")}, {"b", first}, {"b", nil}, {"b", first},
	} {
		got, err := cache.decode(snapshot.path, snapshot.raw)
		want, wantErr := uncachedSubmissionRecords(snapshot.raw)
		if (err == nil) != (wantErr == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot path=%s: records=%+v error=%v; want=%+v error=%v", snapshot.path, got, err, want, wantErr)
		}
		if len(got) > 0 && got[0].Data.InputEpoch != nil {
			*got[0].Data.InputEpoch = 999
		}
	}
}

func BenchmarkSubmissionRecordSnapshots(b *testing.B) {
	line, err := json.Marshal(map[string]any{"kind": "submission_accepted", "data": map[string]any{"text": string(bytes.Repeat([]byte("x"), 1024*1024))}})
	if err != nil {
		b.Fatal(err)
	}
	raw := bytes.Repeat(append(line, '\n'), 8)
	for _, cached := range []bool{false, true} {
		name := map[bool]string{false: "uncached", true: "cached"}[cached]
		b.Run(name, func(b *testing.B) {
			var cache submissionRecordCache
			if cached {
				if _, err := cache.decode("journal", raw); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if cached {
					_, err = cache.decode("journal", raw)
				} else {
					_, err = uncachedSubmissionRecords(raw)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
