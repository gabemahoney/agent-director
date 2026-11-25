package main

import (
	"flag"
	"io"
	"os"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// forceDeliveryHandlerWith implements `agent-director force-delivery`.
func forceDeliveryHandlerWith(client *pkgapi.Client, args []string) error {
	var p pkgapi.ForceDeliveryParams
	fs := flag.NewFlagSet("force-delivery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.InstanceID, "instance-id", "", "id of the agent whose Held Messages to force")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", flagErr(fs, err).Error())
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
	fs := flag.NewFlagSet("get-force", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.ForceID, "force-id", "", "a force_result's force_id")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", flagErr(fs, err).Error())
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
	fs := flag.NewFlagSet("get-delivery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&p.CallerUUID, "caller-uuid", "", "the send's Caller UUID")
	fs.StringVar(&p.DeliveryRequestID, "delivery-request-id", "", "the send's Message ID")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", flagErr(fs, err).Error())
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
