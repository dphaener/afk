package webpush

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// vapidLifetime is how long a VAPID token is good for. RFC 8292 §2 caps exp
// at 24 hours after the request; 12 hours keeps well inside that cap even
// with a machine clock a few hours fast.
const vapidLifetime = 12 * time.Hour

// vapidHeader is the JWT header of every token: ES256 is the only algorithm
// RFC 8292 §2 allows. It is a fixed string rather than a marshalled struct
// so its bytes, and so the signed input, never depend on field order.
const vapidHeader = `{"typ":"JWT","alg":"ES256"}`

// vapidClaims is the token's body (RFC 8292 §2): aud is the push service's
// origin, exp the expiry in Unix seconds, sub a contact for the sender.
type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// vapidAuthorization is the Authorization header value for one push to
// endpoint: "vapid t=<jwt>, k=<public key>" (RFC 8292 §3).
//
// The audience is the endpoint's origin, scheme://host[:port], with the
// path and query dropped: RFC 8292 §2 makes aud the origin of the push
// resource, and the path is the subscription's own capability. The port
// is kept because it is part of the origin (an httptest server in tests, or
// a push service on a non-default port).
//
// subject is the sub claim, passed through as given. It must not be empty:
// Apple's push service refuses a token with no contact, so an empty one
// would fail on every iPhone later rather than here (docs/decisions.md
// T15.4, A3; the sender supplies the default).
//
// The signature is ES256 in JWS form (RFC 7518 §3.4): the raw 64 bytes
// r || s, each a 32-byte big-endian integer, not the ASN.1 DER form
// crypto/ecdsa's SignASN1 produces, which JWS does not allow. It is signed
// with crypto/rand.Reader; ECDSA needs fresh randomness for every signature.
//
// No error carries the endpoint: it is the subscription's capability URL,
// and anyone holding it can send to the device (given the key).
func vapidAuthorization(key Key, endpoint string, subject string, now time.Time) (string, error) {
	if key.priv == nil || key.pub == nil {
		return "", errors.New("no push key to sign with")
	}
	if subject == "" {
		return "", errors.New("the push contact (sub) is empty")
	}
	aud, err := endpointOrigin(endpoint)
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(vapidClaims{
		Aud: aud,
		Exp: now.Add(vapidLifetime).Unix(),
		Sub: subject,
	})
	if err != nil {
		return "", fmt.Errorf("encoding the push token: %w", err)
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString([]byte(vapidHeader)) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key.priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing the push token: %w", err)
	}
	// FillBytes left-pads with zeros; r or s below 2^248 would otherwise
	// come out shorter than 32 bytes and break the fixed r || s layout.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signingInput + "." + enc.EncodeToString(sig) + ", k=" + key.PublicKey(), nil
}

// endpointOrigin returns scheme://host[:port] of an absolute http or https
// URL, and an error for anything else. http is allowed, not only https,
// because tests send to an httptest server on loopback; real push services
// are all https, and the device list (W4.T1) is where plain http is limited
// to loopback.
func endpointOrigin(endpoint string) (string, error) {
	// url.Parse's error quotes the input, so it is dropped.
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Opaque != "" || u.User != nil {
		return "", errors.New("the push endpoint is not an absolute https or http address")
	}
	return u.Scheme + "://" + u.Host, nil
}
