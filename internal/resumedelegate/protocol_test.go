package resumedelegate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// b.kii / SR-9.1: this literal is the permanent caller ABI, independent of
// today's encoder. Future formats must keep accepting this format-1 request.
const format1Request = `{"request_format":1,"response_format":1,"params":{"instance_id":"old-row","confirmed_delivery":false,"confirm_from_transcripts":false},"home":"/scratch/home","store_path":"/scratch/custom/state.db","store_id":"store-1","config_path":"/scratch/custom/config.toml","default_config":false,"tmux_command":"/scratch/bin/tmux","caller":{"process":"caller","pid":42,"hostname":"host","user":"operator","parent_instance_id":"parent"},"deadline":"2030-01-02T03:04:05Z"}`

func TestResumeDelegateFormat1(t *testing.T) {
	r, err := DecodeRequest(strings.NewReader(format1Request))
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		InstanceID  string  `json:"instance_id"`
		Confirmed   *bool   `json:"confirmed_delivery"`
		Transcripts *bool   `json:"confirm_from_transcripts"`
		SendMode    *string `json:"send_mode"`
	}
	if err := DecodeParams(r.Params, &params); err != nil || params.InstanceID != "old-row" || params.Confirmed == nil || *params.Confirmed || params.Transcripts == nil || *params.Transcripts || params.SendMode != nil {
		t.Fatalf("omitted and explicit false parameters: %+v, %v", params, err)
	}
	for _, tc := range []struct{ name, wire string }{
		{"success", `{"response_format":1,"result":{"instance_id":"old-row","pre_trust":"skipped","future":9007199254740993,"nullable":null,"enabled":false}}`},
		{"known", `{"response_format":1,"error":{"err_name":"ErrSpawnNotResumable","err_description":"no covering version","err_details":{"cause":"covering_version_unavailable","provider":"codex"}}}`},
		{"unknown", `{"response_format":1,"error":{"err_name":"ErrFutureResume","err_description":"future description","err_details":{"future":9007199254740993,"nullable":null,"enabled":false}}}`},
		{"absent details", `{"response_format":1,"error":{"err_name":"ErrInternal","err_description":"outcome unknown"}}`},
		{"null details", `{"response_format":1,"error":{"err_name":"ErrFutureResume","err_description":"future description","err_details":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := DecodeResponse(strings.NewReader(tc.wire), r.ResponseFormat)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := Encode(&output, response); err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(output.String()) != tc.wire {
				t.Fatalf("changed response:\n%s\nwant %s", &output, tc.wire)
			}
		})
	}
	var output bytes.Buffer
	if err := Encode(&output, r); err != nil {
		t.Fatal(err)
	}
	copy, err := DecodeRequest(&output)
	if err != nil || !reflect.DeepEqual(copy, r) {
		t.Fatalf("references changed: %+v, %v", copy, err)
	}
}

func TestResumeDelegateFormat1Refusals(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"request version", `"request_format":1`, `"request_format":2`},
		{"response version", `"response_format":1`, `"response_format":2`},
		{"relative home", `"/scratch/home"`, `"home"`},
		{"relative store", `"/scratch/custom/state.db"`, `"state.db"`},
		{"missing store identity", `"store-1"`, `""`},
		{"ambiguous config", `"default_config":false`, `"default_config":true`},
		{"no config marker", `"/scratch/custom/config.toml"`, `""`},
		{"invalid caller", `"pid":42`, `"pid":0`},
		{"NUL reference", `"/scratch/bin/tmux"`, `"/scratch/\u0000tmux"`},
		{"snapshot not references", `"tmux_command":`, `"parsed_config":{},"tmux_command":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRequest(strings.NewReader(strings.Replace(format1Request, tc.old, tc.replacement, 1))); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	defaultConfig := strings.Replace(strings.Replace(format1Request, `"default_config":false`, `"default_config":true`, 1), `"/scratch/custom/config.toml"`, `""`, 1)
	if _, err := DecodeRequest(strings.NewReader(defaultConfig)); err != nil {
		t.Fatalf("explicit default config marker: %v", err)
	}
	for _, wire := range []string{"{", format1Request + "{}", strings.Repeat(" ", MaxBytes+1)} {
		if _, err := DecodeRequest(strings.NewReader(wire)); err == nil {
			t.Fatal("accepted malformed, trailing or oversized request")
		}
	}
	for _, wire := range []string{`{"instance_id":null}`, `{"confirmed_delivery":null}`, `{"confirm_from_transcripts":null}`, `{"send_mode":null}`, `[]`} {
		if err := DecodeParams(json.RawMessage(wire), new(map[string]any)); err == nil {
			t.Fatalf("accepted params %s", wire)
		}
	}
	for _, wire := range []string{`{"response_format":2,"result":{}}`, `{"response_format":1,"result":null}`, `{"response_format":1,"result":{},"error":{"err_name":"ErrInternal","err_description":"x"}}`, `{"response_format":1,"error":{"err_name":"ErrInternal","err_description":"x","err_details":[]}}`, `{"response_format":1,"error":{"err_name":"ErrInternal"}}`} {
		if _, err := DecodeResponse(strings.NewReader(wire), 1); err == nil {
			t.Fatalf("accepted response %s", wire)
		}
	}
}

// CR4: MaxBytes is the complete wire message INCLUDING Encode's newline.
// Pin both request and response paths independently of today's encoder.
func TestResumeDelegateMaximumWireSize(t *testing.T) {
	for _, response := range []bool{false, true} {
		base, marker := format1Request, `"hostname":"host"`
		if response {
			base, marker = `{"response_format":1,"result":{"instance_id":"row","pre_trust":"skipped","padding":""}}`, `"padding":""`
		}
		for _, extra := range []int{0, 1} {
			name := "request"
			if response {
				name = "response"
			}
			t.Run(fmt.Sprintf("%s/extra=%d", name, extra), func(t *testing.T) {
				padded := strings.Replace(base, marker, marker[:len(marker)-1]+strings.Repeat("x", MaxBytes-len(base)-1+extra)+`"`, 1)
				wire := padded + "\n"
				var err error
				if response {
					_, err = DecodeResponse(strings.NewReader(wire), 1)
				} else {
					_, err = DecodeRequest(strings.NewReader(wire))
				}
				if (err != nil) != (extra != 0) {
					t.Fatalf("decode %d-byte wire: %v", len(wire), err)
				}
				var output bytes.Buffer
				err = Encode(&output, json.RawMessage(padded))
				if extra != 0 {
					if err == nil || output.Len() != 0 {
						t.Fatalf("Encode accepted %d-byte wire", output.Len())
					}
				} else {
					if err != nil || output.String() != wire {
						t.Fatalf("Encode boundary: len=%d err=%v", output.Len(), err)
					}
					if response {
						_, err = DecodeResponse(&output, 1)
					} else {
						_, err = DecodeRequest(&output)
					}
					if err != nil {
						t.Fatalf("own encoded boundary cannot round-trip: %v", err)
					}
				}
			})
		}
	}
}
