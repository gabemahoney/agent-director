package spawn

import "github.com/gabemahoney/agent-director/internal/tmux"

// LaunchPreflightError marks a refusal before the launch-row mutation. The API
// maps a caller's fresh/reused request to ErrInvalidFlags and saved resume
// settings to ErrTmuxSessionCreate. Final create-boundary refusals do not use it.
type LaunchPreflightError struct{ cause error }

func (e *LaunchPreflightError) Error() string {
	return e.cause.Error() + "; nothing was launched and no row was inserted or changed"
}

func (e *LaunchPreflightError) Unwrap() error { return e.cause }

// createSizeChecker is optional on injected tmux doubles, which do not exec a
// client. A production *tmux.Client supplies its actual configured binary.
type createSizeChecker interface {
	CheckNewSessionSize(string, string, string, map[string]string, []string, string, string, string, []string, []string, []string, uint64) error
}

// CheckCreateSize checks the complete composed request before files, pre-trust
// and the row mutation. The client, when supplied, provides its actual binary;
// injected doubles without an exec use the default tmux binary. Native
// preparation still needs the defensive check at create.
func CheckCreateSize(req CreateRequest, clients ...LaunchTmux) error {
	var checker createSizeChecker = new(tmux.Client)
	if len(clients) > 0 {
		if actual, ok := clients[0].(createSizeChecker); ok {
			checker = actual
		}
	}
	unset := req.UnsetEnv
	if unset == nil {
		unset = req.ExcludeEnv
	}
	if err := checker.CheckNewSessionSize(req.Socket, req.Name, req.CWD, req.Env, req.Command, req.Token, req.InstanceID, req.StoreID, req.ExcludeEnv, unset, req.ForwardEnv, 0); err != nil {
		return &LaunchPreflightError{cause: err}
	}
	return nil
}
