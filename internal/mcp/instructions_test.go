package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/helptext"
	"github.com/gabemahoney/agent-director/internal/mcp"
)

func TestInitializeInstructionsAndAcknowledgement(t *testing.T) {
	plain, err := helptext.Plain()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary omitted", true: "scoped canonical"}[enabled], func(t *testing.T) {
			out := new(bytes.Buffer)
			calls := 0
			options := mcp.Options{}
			if enabled {
				options = mcp.Options{Instructions: plain, Initialized: func() error {
					if !bytes.Contains(out.Bytes(), []byte(`"id":"current"`)) {
						t.Error("callback preceded correlated response write")
					}
					calls++
					return nil
				}}
			}
			in := `{"method":"notifications/initialized"}` + "\n" + `{"method":"initialize"}` + "\n" + `{"id":"current","method":"initialize"}` + "\n" + `{"id":9,"method":"notifications/initialized"}` + "\n" + `{"method":"notifications/initialized"}` + "\n" + `{"method":"notifications/initialized"}` + "\n"
			server := mcp.New(&fakeDispatcher{}, nil)
			if enabled {
				server = mcp.NewWithOptions(&fakeDispatcher{}, nil, options)
			}
			if err := server.Serve(context.Background(), strings.NewReader(in), out); err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID     string                     `json:"id"`
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != "current" {
				t.Fatalf("correlation=%q", response.ID)
			}
			raw, present := response.Result["instructions"]
			if present != enabled {
				t.Fatalf("instructions presence=%v", present)
			}
			if enabled {
				var got string
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got != plain || !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
					t.Fatal("decoded instructions differ from newline-inclusive canonical help")
				}
				if calls != 1 {
					t.Fatalf("acknowledgements=%d", calls)
				}
			}
		})
	}
}

type failedInitializeWriter struct{}

func (failedInitializeWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestInitializeObservationRequiresSuccessfulWriteAndPropagatesFailure(t *testing.T) {
	for _, failedWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback failure", true: "response write failure"}[failedWrite], func(t *testing.T) {
			want := errors.New("ownership retired")
			calls := 0
			var out io.Writer = new(bytes.Buffer)
			if failedWrite {
				out = failedInitializeWriter{}
				want = io.ErrClosedPipe
			}
			err := mcp.NewWithOptions(&fakeDispatcher{}, nil, mcp.Options{Instructions: "guidance\n", Initialized: func() error { calls++; return want }}).Serve(context.Background(), strings.NewReader("{\"id\":1,\"method\":\"initialize\"}\n{\"method\":\"notifications/initialized\"}\n"), out)
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want %v", err, want)
			}
			if failedWrite && calls != 0 || !failedWrite && calls != 1 {
				t.Fatalf("callbacks=%d", calls)
			}
		})
	}
}
