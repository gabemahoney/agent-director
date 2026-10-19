package main

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
)

// Preserve the native command's input, output and exit status even when
// capacity recording fails. The original command is never parsed as AD argv.
func forwardContextStatusline(encoded string, stdin io.Reader) int {
	if encoded == "" {
		return 0
	}
	command, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return 0
	}
	cmd := exec.Command("/bin/sh", "-c", string(command))
	cmd.Stdin = stdin
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}
