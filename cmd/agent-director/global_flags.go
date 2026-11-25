package main

import (
	"github.com/gabemahoney/agent-director/internal/clisetup"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// parseCommandGlobalFlags resolves globals without parsing verb values or
// opening a client. Handlers still own validation and execution (b.t1b).
func parseCommandGlobalFlags(argv []string) (clisetup.GlobalFlags, []string, error) {
	g, args, err := clisetup.ParseGlobalFlags(argv)
	if err != nil || len(args) == 0 {
		return g, args, err
	}
	verb := args[0]
	if alias, ok := verbAliases[verb]; ok {
		verb = alias
	}
	g, tail, err := parseVerbGlobalFlags(verb, g, args[1:])
	if err != nil {
		return clisetup.GlobalFlags{}, nil, err
	}
	return g, append([]string{args[0]}, tail...), nil
}

func parseVerbGlobalFlags(verb string, g clisetup.GlobalFlags, args []string) (clisetup.GlobalFlags, []string, error) {
	switch verb {
	case "help", "version":
		return clisetup.ParseVerbGlobalFlags(nil, g, args)
	case "get-many":
		// Every post-verb word is an ID, including -- and global names.
		return g, args, nil
	case "trail-emit":
		g, rest, err := clisetup.ParseVerbGlobalFlags(nil, g, args)
		if err != nil || len(rest) == 0 || rest[0] != "relay-attempt" {
			return g, rest, err
		}
		g, tail, err := clisetup.ParseVerbGlobalFlags(trailEmitRelayAttemptFlagSet(new(string), new(string), new(string), new(int), new(int), new(string)), g, rest[1:])
		if err != nil {
			return clisetup.GlobalFlags{}, nil, err
		}
		return g, append([]string{rest[0]}, tail...), nil
	case "spawn":
		return clisetup.ParseVerbGlobalFlags(spawnFlagSet(new(pkgapi.SpawnParams), new(map[string]string), new(map[string]string), new([]string), new([]string), new([]string)), g, args)
	case "status":
		return clisetup.ParseVerbGlobalFlags(statusFlagSet(new(string)), g, args)
	case "send-keys":
		return clisetup.ParseVerbGlobalFlags(sendKeysFlagSet(new(pkgapi.SendKeysParams)), g, args)
	case "record-pane-answer":
		return clisetup.ParseVerbGlobalFlags(recordPaneAnswerFlagSet(new(pkgapi.RecordPaneAnswerParams)), g, args)
	case "read-pane":
		return clisetup.ParseVerbGlobalFlags(readPaneFlagSet(new(pkgapi.ReadPaneParams)), g, args)
	case "make-template":
		return clisetup.ParseVerbGlobalFlags(makeTemplateFlagSet(new(pkgapi.MakeTemplateParams), new(map[string]string), new(map[string]string), new([]string), new([]string), new([]string), new([]string)), g, args)
	case "list":
		return clisetup.ParseVerbGlobalFlags(listFlagSet(new(pkgapi.ListParams), new(string), new([]string)), g, args)
	case "pause":
		return clisetup.ParseVerbGlobalFlags(pauseFlagSet(new(pkgapi.PauseParams)), g, args)
	case "decide":
		return clisetup.ParseVerbGlobalFlags(decideFlagSet(new(pkgapi.DecideParams)), g, args)
	case "resume":
		return clisetup.ParseVerbGlobalFlags(resumeFlagSet(new(pkgapi.ResumeParams)), g, args)
	case "kill":
		return clisetup.ParseVerbGlobalFlags(killFlagSet(new(pkgapi.KillParams)), g, args)
	case "get":
		return clisetup.ParseVerbGlobalFlags(getFlagSet(new(string)), g, args)
	case "list-subagents":
		return clisetup.ParseVerbGlobalFlags(listSubagentsFlagSet(new(string)), g, args)
	case "get-permission":
		return clisetup.ParseVerbGlobalFlags(getPermissionFlagSet(new(pkgapi.GetPermissionParams)), g, args)
	case "force-delivery":
		return clisetup.ParseVerbGlobalFlags(forceDeliveryFlagSet(new(pkgapi.ForceDeliveryParams)), g, args)
	case "get-force":
		return clisetup.ParseVerbGlobalFlags(getForceFlagSet(new(pkgapi.GetForceParams)), g, args)
	case "get-delivery":
		return clisetup.ParseVerbGlobalFlags(getDeliveryFlagSet(new(pkgapi.GetDeliveryParams)), g, args)
	case "sweep":
		return clisetup.ParseVerbGlobalFlags(sweepFlagSet(), g, args)
	case "find-missing":
		return clisetup.ParseVerbGlobalFlags(findMissingFlagSet(), g, args)
	case "expire":
		return clisetup.ParseVerbGlobalFlags(expireFlagSet(new(string)), g, args)
	case "serve":
		return clisetup.ParseVerbGlobalFlags(serveFlagSet(new(bool)), g, args)
	case "cursor-host":
		return clisetup.ParseVerbGlobalFlags(cursorHostFlagSet(new(string), new(string), new(string), new(string), new(string)), g, args)
	default:
		return g, args, nil
	}
}
