package atprotoauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/golang-jwt/jwt/v5"
)

const MaxServiceAuthLifetime = 5 * time.Minute

var (
	ErrMalformed = errors.New("atprotoauth: malformed service-auth token")

	ErrExpired = errors.New("atprotoauth: service-auth token expired")

	ErrBadSignature = errors.New("atprotoauth: service-auth signature does not match the issuer")

	ErrNoKey = errors.New("atprotoauth: could not resolve the issuer's signing key")

	ErrBadAudience = errors.New("atprotoauth: service-auth token is for another service")

	ErrBadLxm = errors.New("atprotoauth: service-auth token is for another lexicon method")

	ErrBadIssuer = errors.New("atprotoauth: the issuer's DID method is not admitted at this door")

	ErrLifetimeTooLong = errors.New("atprotoauth: service-auth token lifetime exceeds the maximum")
)

func Verify(
	ctx context.Context,
	token string,
	wantLxm string,
	audiences []string,
	dir identity.Directory,
) (string, error) {
	if wantLxm == "" {
		return "", fmt.Errorf("%w: no lexicon method was required", ErrBadLxm)
	}
	nsid, err := syntax.ParseNSID(wantLxm)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not an NSID: %v", ErrBadLxm, wantLxm, err)
	}
	if len(audiences) == 0 {
		return "", fmt.Errorf("%w: no audience is configured", ErrBadAudience)
	}

	var lastAud error
	for _, aud := range audiences {
		v := &auth.ServiceAuthValidator{Audience: aud, Dir: admitPlcAndWeb{dir}}
		did, err := v.Validate(ctx, token, &nsid)
		if err == nil {
			if err := checkLifetime(token); err != nil {
				return "", err
			}
			return did.String(), nil
		}
		if errors.Is(err, jwt.ErrTokenInvalidAudience) {
			lastAud = err
			continue
		}
		return "", classify(err)
	}

	return "", fmt.Errorf("%w: %v", ErrBadAudience, lastAud)
}

// ponytail: a validated token stays replayable until its short exp; if replay ever matters, keep a jti set keyed by exp and evict each entry at its exp.
func checkLifetime(token string) error {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return fmt.Errorf("%w: no expiry claim", ErrMalformed)
	}
	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil {
		return fmt.Errorf("%w: no issued-at claim", ErrMalformed)
	}
	if lifetime := exp.Time.Sub(iat.Time); lifetime > MaxServiceAuthLifetime {
		return fmt.Errorf("%w: token lifetime %s exceeds the %s maximum", ErrLifetimeTooLong, lifetime, MaxServiceAuthLifetime)
	}
	return nil
}

func classify(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return fmt.Errorf("%w: %v", ErrExpired, err)
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return fmt.Errorf("%w: %v", ErrBadAudience, err)
	case errors.Is(err, jwt.ErrTokenInvalidClaims):
		return fmt.Errorf("%w: %v", ErrBadLxm, err)
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	case errors.Is(err, ErrBadIssuer):
		return err
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return fmt.Errorf("%w: %v", ErrNoKey, err)
	default:
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
}
