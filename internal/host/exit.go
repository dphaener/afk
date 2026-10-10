package host

// The afk attach exit-code contract. Defined once here so the CLI
// (`afk attach`, W3.T4) and the dashboard (W4.T1) share a single source of
// truth instead of re-deriving these values independently. Any exit path not
// covered below is a generic failure (exit code 1).
const (
	// ExitDetached: the user pressed ctrl-\ to cleanly detach.
	ExitDetached = 0
	// ExitStolen: another client attached, stealing the terminal.
	ExitStolen = 10
	// ExitSessionEnded: the host's child process exited on its own.
	ExitSessionEnded = 11
	// ExitNotRunning: no host is running for the requested session.
	ExitNotRunning = 12
	// ExitDropped: the connection closed with no Exit frame, so the session
	// did not say it ended. Usually the host dropped a client too far behind
	// its output; the session may well still be running.
	ExitDropped = 13
)
