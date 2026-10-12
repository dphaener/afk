package ship

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// HealthTimeout bounds the one request a health check makes. A deploy that is
// still starting often accepts the connection and then says nothing; the
// daemon asks again on a later tick, so there is no reason to wait long now.
const HealthTimeout = 10 * time.Second

// healthBodyLimit is how much of the answer is read before the connection is
// let go. Only the status matters; reading a little lets the connection be
// reused, and the limit stops a health page that never ends from holding it.
const healthBodyLimit = 64 << 10

// NewHealth returns the function for Deps.Health: one GET, healthy on any 2xx
// status. A nil client means http.DefaultClient; either way the request
// carries its own 10-second deadline, so a client with no timeout cannot hang
// a tick.
//
// An answer with any other status is "not healthy" with a nil error: the
// deploy answered, and what it said is the fact. An error means there was no
// answer at all. Its text never holds the URL, because a health URL can carry
// a password or a token and the error may be logged: net/http wraps its
// errors in *url.Error, whose text quotes the URL, so that wrapper is taken
// off here.
func NewHealth(client *http.Client) func(rawURL string) (bool, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return func(rawURL string) (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), HealthTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return false, withoutURL(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, withoutURL(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, healthBodyLimit))
		return resp.StatusCode >= 200 && resp.StatusCode <= 299, nil
	}
}

func withoutURL(err error) error {
	var ue *url.Error
	// A URL that cannot be parsed is quoted inside the inner error too, so
	// that one gets the fixed text.
	if errors.As(err, &ue) && ue.Err != nil && ue.Op != "parse" {
		return errors.New("the health check got no answer: " + ue.Err.Error())
	}
	return errors.New("the health check could not be made: the health URL is not a usable web address")
}
