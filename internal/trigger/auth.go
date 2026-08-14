package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"

	"github.com/arhuman/p6e/internal/node"
)

// CodeUnauthorized is the error code a trigger reports when an event failed
// authentication. It is named rather than matched on message text, because the
// HTTP layer maps it to a status: see daemon.statusFor.
const CodeUnauthorized = "unauthorized"

// SchemeHMACSHA256 is the only signature scheme V0 implements. It is what
// GitHub, Stripe and most of the ecosystem use: HMAC-SHA256 over the raw
// request body, keyed by a shared secret, hex encoded in a header.
const SchemeHMACSHA256 = "hmac-sha256"

// authConfig is a webhook trigger's `auth` block.
//
// The secret is named, not inlined. A pipeline file sits in a directory that is
// deployed and read like a crontab, so it is the wrong place for a credential;
// naming an environment variable also keeps `p6e check` free of secrets, the
// same bargain env.get makes.
type authConfig struct {
	// Scheme selects the signature algorithm. Only hmac-sha256 exists today.
	Scheme string `yaml:"scheme"`
	// Header is where the signature arrives, for example
	// "X-Hub-Signature-256".
	Header string `yaml:"header"`
	// Prefix is what the sender puts before the hex digest, for example
	// "sha256=". Empty means the header holds the digest alone.
	Prefix string `yaml:"prefix"`
	// SecretEnv names the environment variable holding the shared secret. It is
	// read per request, not at compile time, so a pipeline whose secret lives
	// only in production still validates anywhere.
	SecretEnv string `yaml:"secret_env"`
}

// compile validates the block and returns the verifier it describes, or nil
// when no auth was configured.
//
// Everything checkable without the secret is checked here, at compile time, so
// a malformed auth block fails `p6e check` rather than the first real event.
func (c *authConfig) compile(capability string) (*verifier, error) {
	if c == nil {
		return nil, nil
	}
	if c.Scheme != SchemeHMACSHA256 {
		return nil, node.Errf(node.KindInvalidInput, "bad_auth",
			"%s auth.scheme %q is not supported (accepted: %s)", capability, c.Scheme, SchemeHMACSHA256)
	}
	if c.Header == "" {
		return nil, node.Errf(node.KindInvalidInput, "bad_auth",
			"%s auth requires a header, such as \"X-Hub-Signature-256\"", capability)
	}
	if c.SecretEnv == "" {
		return nil, node.Errf(node.KindInvalidInput, "bad_auth",
			"%s auth requires secret_env: the environment variable holding the shared secret", capability)
	}
	return &verifier{header: c.Header, prefix: c.Prefix, secretEnv: c.SecretEnv}, nil
}

// verifier checks one request's signature.
type verifier struct {
	header    string
	prefix    string
	secretEnv string
}

// verify reports whether the request carries a valid signature over body.
//
// Every rejection returns the same message and the same code. Telling a caller
// whether the header was missing, malformed, or merely wrong tells an attacker
// which half of the problem to work on, and the daemon's log records the
// specific reason for the operator who is entitled to it.
func (v *verifier) verify(r *http.Request, body []byte) (reason string, err *node.Error) {
	secret := os.Getenv(v.secretEnv)
	if secret == "" {
		// A misconfigured daemon is not an unauthorized caller, and saying so
		// is what stops an operator hunting a signature bug that is really an
		// unset variable.
		return "", node.Errf(node.KindInternal, "secret_unset",
			"the shared secret is unavailable: %s is unset or empty", v.secretEnv)
	}

	presented := r.Header.Get(v.header)
	if presented == "" {
		return "no " + v.header + " header", unauthorized()
	}
	digest, ok := strings.CutPrefix(presented, v.prefix)
	if !ok {
		return v.header + " does not carry the expected " + v.prefix + " prefix", unauthorized()
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	// Constant time, so the comparison does not leak the expected digest one
	// byte at a time to a caller willing to measure.
	if !hmac.Equal([]byte(strings.ToLower(digest)), []byte(expected)) {
		return "signature does not match the body", unauthorized()
	}
	return "", nil
}

func unauthorized() *node.Error {
	return node.Errf(node.KindPermanent, CodeUnauthorized, "unauthorized")
}
