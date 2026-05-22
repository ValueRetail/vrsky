package main

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SHA-1 still required by some webhook providers (GitHub legacy header).
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
)

// signatureConfig is the runtime form of WebhookSignatureConfig from the
// management-api schema. The Secret field is the *plaintext* shared secret,
// resolved at connection-start time via crypto.ResolveSecrets.
type signatureConfig struct {
	Header    string
	Algorithm string
	Encoding  string
	Prefix    string
	Secret    string
}

// Errors surfaced to the caller. handleWebhook maps each to 401.
var (
	errMissingHeader      = errors.New("signature header missing")
	errEmptyHeader        = errors.New("signature header empty")
	errMalformedSignature = errors.New("signature has unexpected encoding")
	errSignatureMismatch  = errors.New("signature does not match payload")
	errUnknownAlgorithm   = errors.New("unsupported signature algorithm")
	errUnknownEncoding    = errors.New("unsupported signature encoding")
	errEmptySecret        = errors.New("signing secret not configured")
)

// verifyHMAC checks that headerValue is a valid HMAC of body produced with
// cfg.Secret. Returns nil on a valid signature. All comparison is done in
// constant time to keep timing attacks out of scope.
func verifyHMAC(body []byte, headerValue string, cfg signatureConfig) error {
	if cfg.Secret == "" {
		return errEmptySecret
	}
	if headerValue == "" {
		return errEmptyHeader
	}

	// Strip the optional algorithm prefix, e.g. "sha256=" used by GitHub.
	if cfg.Prefix != "" {
		if !strings.HasPrefix(headerValue, cfg.Prefix) {
			return errMalformedSignature
		}
		headerValue = strings.TrimPrefix(headerValue, cfg.Prefix)
	}

	provided, err := decodeMAC(headerValue, cfg.Encoding)
	if err != nil {
		return err
	}

	mac, err := newMAC(cfg.Algorithm, []byte(cfg.Secret))
	if err != nil {
		return err
	}
	mac.Write(body)
	expected := mac.Sum(nil)

	if !hmac.Equal(expected, provided) {
		return errSignatureMismatch
	}
	return nil
}

func newMAC(algorithm string, key []byte) (hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case "hmac-sha1", "sha1":
		return hmac.New(sha1.New, key), nil
	case "hmac-sha256", "sha256", "":
		return hmac.New(sha256.New, key), nil
	case "hmac-sha512", "sha512":
		return hmac.New(sha512.New, key), nil
	default:
		return nil, errUnknownAlgorithm
	}
}

func decodeMAC(s, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "hex", "":
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, errMalformedSignature
		}
		return b, nil
	case "base64":
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			// Allow URL-safe base64 too (some providers do this).
			b, err = base64.URLEncoding.DecodeString(s)
			if err != nil {
				return nil, errMalformedSignature
			}
		}
		return b, nil
	default:
		return nil, errUnknownEncoding
	}
}
