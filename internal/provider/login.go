package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrLoginUnsupported means the provider has no way to report whether its CLI
// is logged in. Callers treat it as "nothing to check", not as a failure.
var ErrLoginUnsupported = errors.New("this provider cannot report its login")

// LoginDeps carries the outside world into Login. The runner is a parameter
// for the same reason it is on UsageDeps: the package stays free of os/exec
// and the matrix is table-testable without a CLI installed.
type LoginDeps struct {
	Run CmdFunc
}

// LoginStatus is what the login check found.
//
// It has no expiry field on purpose. The CLI's status output carries no date
// key (keys seen on 2.1.296: loggedIn, authMethod, apiProvider,
// subscriptionType, email, orgId, orgName, analyticsDisabled,
// projectsDirectory, configDirectory), so any expiry would be a guess. The
// only thing known is whether the CLI says it is logged in right now.
type LoginStatus struct {
	LoggedIn bool
}

// Login runs `claude auth status`, which prints JSON by default (so no format
// flag is passed), and reads the boolean loggedIn key.
//
// Output that parses wins over the exit status: the CLI exits 1 when logged
// out and still prints JSON (PLAN 6.2). Output with no boolean loggedIn is an
// error and never "logged out", because treating an unreadable answer as a
// logout would raise a false alarm every time the CLI changes shape or is
// missing.
func (claude) Login(ctx context.Context, d LoginDeps) (LoginStatus, error) {
	if d.Run == nil {
		return LoginStatus{}, errors.New("login check: no command runner")
	}
	out, runErr := d.Run(ctx, "claude", "auth", "status")
	var raw struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	parseErr := json.Unmarshal(bytes.TrimSpace(out), &raw)
	if parseErr == nil && raw.LoggedIn != nil {
		return LoginStatus{LoggedIn: *raw.LoggedIn}, nil
	}
	if runErr != nil {
		return LoginStatus{}, fmt.Errorf("run claude auth status: %w", runErr)
	}
	if parseErr != nil {
		return LoginStatus{}, fmt.Errorf("claude auth status gave no JSON: %w", parseErr)
	}
	return LoginStatus{}, errors.New("claude auth status output has no loggedIn key")
}

func (codex) Login(context.Context, LoginDeps) (LoginStatus, error) {
	return LoginStatus{}, ErrLoginUnsupported
}

func (opencode) Login(context.Context, LoginDeps) (LoginStatus, error) {
	return LoginStatus{}, ErrLoginUnsupported
}
