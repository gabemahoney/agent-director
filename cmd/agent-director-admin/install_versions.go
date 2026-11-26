package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/installversion"
)

func versionInstallVerb(verb string) bool {
	switch verb {
	case "version-facts", "publish-version", "rollback-version", "list-versions":
		return true
	}
	return false
}

func versionFactsHandler(args []string) error {
	fs := versionFactsFlagSet()
	if done, err := parseFlags(fs, args); done {
		return err
	}
	return writeJSON(adminapi.VersionFacts())
}

func publishVersionHandler(args []string) error {
	var binary string
	fs := publishVersionFlagSet(&binary)
	if done, err := parseFlags(fs, args); done {
		return err
	}
	if binary == "" {
		return writeError(errInvalidFlags, "--binary is required")
	}
	root, err := adminapi.VersionInstallRoot()
	if err != nil {
		return writeVersionInstallError(err)
	}
	result, err := adminapi.PublishVersion(context.Background(), root, binary)
	if err != nil {
		return writeVersionInstallError(err)
	}
	return writeJSON(result)
}

func rollbackVersionHandler(args []string) error {
	var stamp string
	fs := rollbackVersionFlagSet(&stamp)
	if done, err := parseFlags(fs, args); done {
		return err
	}
	if stamp == "" {
		return writeError(errInvalidFlags, "--version is required")
	}
	root, err := adminapi.VersionInstallRoot()
	if err != nil {
		return writeVersionInstallError(err)
	}
	result, err := adminapi.RollbackVersion(root, stamp)
	if err != nil {
		return writeVersionInstallError(err)
	}
	return writeJSON(result)
}

func listVersionsHandler(args []string) error {
	fs := listVersionsFlagSet()
	if done, err := parseFlags(fs, args); done {
		return err
	}
	root, err := adminapi.VersionInstallRoot()
	if err != nil {
		return writeVersionInstallError(err)
	}
	result, err := adminapi.ListVersions(root)
	if err != nil {
		return writeVersionInstallError(err)
	}
	return writeJSON(result)
}

func writeVersionInstallError(err error) error {
	var refusal *installversion.Refusal
	if !errors.As(err, &refusal) {
		refusal = installversion.ReadRefusal(err)
	}
	b, err := json.Marshal(errorEnvelope{ErrName: "ErrVersionInstallRefused", ErrDescription: refusal.Error(), Details: refusal})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(os.Stderr, string(b)); err != nil {
		return err
	}
	return errDispatch
}

// versionFactsFlagSet defines version-facts's flags for both global scanning and the handler.
func versionFactsFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("version-facts", flag.ContinueOnError)
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// publishVersionFlagSet defines publish-version's flags for both global scanning and the handler.
func publishVersionFlagSet(binary *string) *flag.FlagSet {
	fs := flag.NewFlagSet("publish-version", flag.ContinueOnError)
	fs.StringVar(binary, "binary", "", "candidate runtime executable (required)")
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// rollbackVersionFlagSet defines rollback-version's flags for both global scanning and the handler.
func rollbackVersionFlagSet(stamp *string) *flag.FlagSet {
	fs := flag.NewFlagSet("rollback-version", flag.ContinueOnError)
	fs.StringVar(stamp, "version", "", "retained version stamp (required)")
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// listVersionsFlagSet defines list-versions's flags for both global scanning and the handler.
func listVersionsFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("list-versions", flag.ContinueOnError)
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}
