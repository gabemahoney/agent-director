// Install-only persistent real Go API caller; no CLI subprocess substitutes.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
)

func main() {
	c, err := api.New(api.Options{})
	if err != nil {
		panic(err)
	}
	defer c.Close()
	out := json.NewEncoder(os.Stdout)
	_ = out.Encode(map[string]any{"ready": true, "pid": os.Getpid()})
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var p api.SendKeysParams
		if err := json.Unmarshal(in.Bytes(), &p); err != nil {
			panic(err)
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		r, err := c.SendKeysContext(ctx, p)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := out.Encode(map[string]any{"result": r, "elapsed_ms": time.Since(start).Milliseconds()}); err != nil {
			panic(err)
		}
	}
	if err := in.Err(); err != nil {
		panic(err)
	}
}
