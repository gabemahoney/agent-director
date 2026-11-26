package main

import (
	"flag"
	"io"
	"os"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// forceDeliveryHandlerWith implements `agent-director force-delivery`.
func forceDeliveryHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.ForceDeliveryParams
	fs := forceDeliveryFlagSet(&p)
	parseErr := fs.Parse(args)
	var repeats clisetup.SingleFlagCheck
	fs.VisitAll(repeats.Visit)
	if err := flagErr(fs, repeats.ParseError(parseErr)); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if err := rejectUnexpectedArgs(fs.Args()); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.InstanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--instance-id is required")
	}
	result, err := client.ForceDelivery(p)
	if err != nil {
		return writeVerbError(err)
	}
	return writeJSON(os.Stdout, result)
}

// getForceHandlerWith implements `agent-director get-force`.
func getForceHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.GetForceParams
	fs := getForceFlagSet(&p)
	parseErr := fs.Parse(args)
	var repeats clisetup.SingleFlagCheck
	fs.VisitAll(repeats.Visit)
	if err := flagErr(fs, repeats.ParseError(parseErr)); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if err := rejectUnexpectedArgs(fs.Args()); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.ForceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--force-id is required")
	}
	result, err := client.GetForce(p)
	if err != nil {
		return writeVerbError(err)
	}
	return writeJSON(os.Stdout, result)
}

// getDeliveryHandlerWith implements `agent-director get-delivery`.
func getDeliveryHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.GetDeliveryParams
	fs := getDeliveryFlagSet(&p)
	parseErr := fs.Parse(args)
	var repeats clisetup.SingleFlagCheck
	fs.VisitAll(repeats.Visit)
	if err := flagErr(fs, repeats.ParseError(parseErr)); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if err := rejectUnexpectedArgs(fs.Args()); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if p.CallerUUID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--caller-uuid is required")
	}
	if p.DeliveryRequestID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--delivery-request-id is required")
	}
	result, err := client.GetDelivery(p)
	if err != nil {
		return writeVerbError(err)
	}
	return writeJSON(os.Stdout, result)
}

// forceDeliveryFlagSet defines force-delivery's flags for both global scanning and the handler.
func forceDeliveryFlagSet(p *pkgapi.ForceDeliveryParams) *flag.FlagSet {
	fs := flag.NewFlagSet("force-delivery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.InstanceID, "instance-id", "", "id of the agent whose Held Messages to force")
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// getForceFlagSet defines get-force's flags for both global scanning and the handler.
func getForceFlagSet(p *pkgapi.GetForceParams) *flag.FlagSet {
	fs := flag.NewFlagSet("get-force", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ForceID, "force-id", "", "a force_result's force_id")
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// getDeliveryFlagSet defines get-delivery's flags for both global scanning and the handler.
func getDeliveryFlagSet(p *pkgapi.GetDeliveryParams) *flag.FlagSet {
	fs := flag.NewFlagSet("get-delivery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.CallerUUID, "caller-uuid", "", "the send's Caller UUID")
	fs.StringVar(&p.DeliveryRequestID, "delivery-request-id", "", "the send's Message ID")
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}
