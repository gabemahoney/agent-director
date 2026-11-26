package main

import (
	"io"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// parseCommandGlobalFlags resolves globals before opening a client. A repeat
// of a verb flag triggers a silent trial parse; handlers retain their
// existing help and parse-error handling (b.t1b, b.51h).
func parseCommandGlobalFlags(argv []string) (clisetup.GlobalFlags, []string, error) {
	g, args, err := clisetup.ParseGlobalFlags(argv)
	if (err != nil && !clisetup.IsGlobalFlagRepeat(err)) || len(args) == 0 {
		return g, args, err
	}
	// Finish validating verb-position globals before reporting a prefix repeat.
	pendingGlobalRepeat := err
	verb := args[0]
	if alias, ok := verbAliases[verb]; ok {
		verb = alias
	}
	g, tail, err := parseVerbGlobalFlags(verb, g, args[1:])
	err = clisetup.GlobalFlagScanError(pendingGlobalRepeat, err)
	if err != nil {
		return clisetup.GlobalFlags{}, nil, err
	}
	return g, append([]string{args[0]}, tail...), nil
}

func parseVerbGlobalFlags(verb string, g clisetup.GlobalFlags, args []string) (clisetup.GlobalFlags, []string, error) {
	switch verb {
	case "help", "version":
		return clisetup.ParseVerbGlobalFlags(nil, g, args, nil)
	case "get-many":
		// Every post-verb word is an ID, including -- and global names.
		return g, args, nil
	case "trail-emit":
		g, rest, err := clisetup.ParseVerbGlobalFlags(nil, g, args, nil)
		if (err != nil && !clisetup.IsGlobalFlagRepeat(err)) || len(rest) == 0 || rest[0] != "relay-attempt" {
			return g, rest, err
		}
		pendingGlobalRepeat := err
		fs := trailEmitRelayAttemptFlagSet(new(string), new(string), new(string), new(int), new(int), new(string))
		g, tail, err := clisetup.ParseVerbGlobalFlags(fs, g, rest[1:], func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
		err = clisetup.GlobalFlagScanError(pendingGlobalRepeat, err)
		if err != nil {
			return clisetup.GlobalFlags{}, nil, err
		}
		return g, append([]string{rest[0]}, tail...), nil
	case "spawn":
		fs := spawnFlagSet(new(pkgapi.SpawnParams), new(map[string]string), new(map[string]string), new([]string), new([]string), new([]string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "status":
		fs := statusFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "send-keys":
		fs := sendKeysFlagSet(new(pkgapi.SendKeysParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "record-pane-answer":
		fs := recordPaneAnswerFlagSet(new(pkgapi.RecordPaneAnswerParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "read-pane":
		fs := readPaneFlagSet(new(pkgapi.ReadPaneParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "make-template":
		fs := makeTemplateFlagSet(new(pkgapi.MakeTemplateParams), new(map[string]string), new(map[string]string), new([]string), new([]string), new([]string), new([]string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "list":
		fs := listFlagSet(new(pkgapi.ListParams), new(string), new([]string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "pause":
		fs := pauseFlagSet(new(pkgapi.PauseParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "decide":
		fs := decideFlagSet(new(pkgapi.DecideParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "resume":
		fs := resumeFlagSet(new(pkgapi.ResumeParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "kill":
		fs := killFlagSet(new(pkgapi.KillParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "get":
		fs := getFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "list-subagents":
		fs := listSubagentsFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "get-permission":
		fs := getPermissionFlagSet(new(pkgapi.GetPermissionParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "force-delivery":
		fs := forceDeliveryFlagSet(new(pkgapi.ForceDeliveryParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "get-force":
		fs := getForceFlagSet(new(pkgapi.GetForceParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "get-delivery":
		fs := getDeliveryFlagSet(new(pkgapi.GetDeliveryParams))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "sweep":
		fs := sweepFlagSet()
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "find-missing":
		fs := findMissingFlagSet()
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "expire":
		fs := expireFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "serve":
		fs := serveFlagSet(new(bool))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "cursor-host":
		fs := cursorHostFlagSet(new(string), new(string), new(string), new(string), new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	default:
		return g, args, nil
	}
}
