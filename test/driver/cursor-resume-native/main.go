// Command cursor-resume-native emits fixture SQL or sidecar JSON; it never opens a database.
package main

import (
	"fmt"
	"os"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix/nativecheckpoint"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: cursor-resume-native sql|metadata ABS_CWD SESSION_ID")
		os.Exit(2)
	}
	sql, metadata, err := nativecheckpoint.Encode(os.Args[2], os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "sql":
		fmt.Print(sql)
	case "metadata":
		fmt.Print(string(metadata))
	default:
		os.Exit(2)
	}
}
