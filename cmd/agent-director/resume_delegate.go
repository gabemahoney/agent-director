package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// runResumeDelegation is private one-shot plumbing, dispatched before global
// setup can load a default config or open a default store. stdout is protocol
// only; malformed requests produce a diagnostic and never execute Resume.
func runResumeDelegation(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "resume delegation: unexpected arguments")
		return 1
	}
	request, err := resumedelegate.DecodeRequest(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !time.Now().Before(request.Deadline) {
		fmt.Fprintln(os.Stderr, "resume delegation: expired request")
		return 1
	}
	// Setup and filesystem calls have no context API. Like the startup wake,
	// this private process cannot survive its inherited absolute deadline.
	stop := time.AfterFunc(time.Until(request.Deadline), func() { os.Exit(1) })
	defer stop.Stop()
	if err := os.Setenv("HOME", request.Home); err != nil {
		fmt.Fprintln(os.Stderr, "resume delegation: cannot apply home")
		return 1
	}
	result, callErr := resumedelegate.Resume(request)
	response := resumedelegate.Response{ResponseFormat: request.ResponseFormat, Result: result}
	if callErr != nil {
		name, description := errnames.Classify(callErr)
		remote := &resumedelegate.Error{Name: name, Description: description}
		if details := pkgapi.ErrDetails(callErr); details != nil {
			remote.Details, err = json.Marshal(details)
			if err != nil {
				fmt.Fprintln(os.Stderr, "resume delegation: cannot encode error details")
				return 1
			}
		}
		response.Result, response.Error = nil, remote
	}
	if err := resumedelegate.Encode(os.Stdout, response); err != nil {
		fmt.Fprintln(os.Stderr, "resume delegation: cannot write response")
		return 1
	}
	return 0
}
