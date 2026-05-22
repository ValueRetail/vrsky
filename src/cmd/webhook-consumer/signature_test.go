package main

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SHA-1 still in production webhook signatures (GitHub legacy).
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

const githubSecret = "It's a Secret to Everybody"

// sample body from the GitHub docs:
// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
const githubBody = "Hello, World!"

// hexSHA256 helper for building expected GitHub-style signatures.
func hexSHA256(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyHMAC_GitHubSha256_Valid(t *testing.T) {
	sig := hexSHA256(githubBody, githubSecret)
	cfg := signatureConfig{
		Header:    "X-Hub-Signature-256",
		Algorithm: "hmac-sha256",
		Encoding:  "hex",
		Prefix:    "sha256=",
		Secret:    githubSecret,
	}
	if err := verifyHMAC([]byte(githubBody), "sha256="+sig, cfg); err != nil {
		t.Fatalf("expected valid GitHub-style signature, got %v", err)
	}
}

func TestVerifyHMAC_WrongSignature(t *testing.T) {
	cfg := signatureConfig{Algorithm: "hmac-sha256", Encoding: "hex", Prefix: "sha256=", Secret: githubSecret}
	bad := "sha256=" + hex.EncodeToString(make([]byte, 32)) // all zeroes
	if err := verifyHMAC([]byte(githubBody), bad, cfg); !errors.Is(err, errSignatureMismatch) {
		t.Fatalf("expected errSignatureMismatch, got %v", err)
	}
}

func TestVerifyHMAC_MissingPrefix(t *testing.T) {
	cfg := signatureConfig{Algorithm: "hmac-sha256", Encoding: "hex", Prefix: "sha256=", Secret: githubSecret}
	if err := verifyHMAC([]byte(githubBody), hexSHA256(githubBody, githubSecret), cfg); !errors.Is(err, errMalformedSignature) {
		t.Fatalf("expected errMalformedSignature when prefix missing, got %v", err)
	}
}

func TestVerifyHMAC_EmptyHeader(t *testing.T) {
	cfg := signatureConfig{Algorithm: "hmac-sha256", Secret: "x"}
	if err := verifyHMAC([]byte("x"), "", cfg); !errors.Is(err, errEmptyHeader) {
		t.Fatalf("expected errEmptyHeader, got %v", err)
	}
}

func TestVerifyHMAC_EmptySecret(t *testing.T) {
	cfg := signatureConfig{Algorithm: "hmac-sha256"}
	if err := verifyHMAC([]byte("x"), "deadbeef", cfg); !errors.Is(err, errEmptySecret) {
		t.Fatalf("expected errEmptySecret, got %v", err)
	}
}

func TestVerifyHMAC_MalformedHex(t *testing.T) {
	cfg := signatureConfig{Algorithm: "hmac-sha256", Encoding: "hex", Secret: "x"}
	if err := verifyHMAC([]byte("body"), "not-hex!", cfg); !errors.Is(err, errMalformedSignature) {
		t.Fatalf("expected errMalformedSignature, got %v", err)
	}
}

func TestVerifyHMAC_Base64(t *testing.T) {
	secret := "shh"
	body := []byte("payload")
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	cfg := signatureConfig{Algorithm: "hmac-sha1", Encoding: "base64", Secret: secret}
	if err := verifyHMAC(body, sig, cfg); err != nil {
		t.Fatalf("base64 sha1 verify: %v", err)
	}
}

func TestVerifyHMAC_URLSafeBase64(t *testing.T) {
	secret := "shh"
	body := []byte(`{"x":1}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	cfg := signatureConfig{Algorithm: "hmac-sha256", Encoding: "base64", Secret: secret}
	if err := verifyHMAC(body, sig, cfg); err != nil {
		t.Fatalf("URL-safe base64 verify: %v", err)
	}
}

func TestVerifyHMAC_UnknownAlgorithm(t *testing.T) {
	cfg := signatureConfig{Algorithm: "md5", Encoding: "hex", Secret: "x"}
	if err := verifyHMAC([]byte("x"), "00", cfg); !errors.Is(err, errUnknownAlgorithm) {
		t.Fatalf("expected errUnknownAlgorithm, got %v", err)
	}
}

func TestVerifyHMAC_UnknownEncoding(t *testing.T) {
	cfg := signatureConfig{Algorithm: "hmac-sha256", Encoding: "morse", Secret: "x"}
	if err := verifyHMAC([]byte("x"), "00", cfg); !errors.Is(err, errUnknownEncoding) {
		t.Fatalf("expected errUnknownEncoding, got %v", err)
	}
}

func TestVerifyHMAC_DefaultAlgorithm(t *testing.T) {
	// Empty algorithm defaults to sha256, matching the most common case.
	secret := "shh"
	body := []byte("hi")
	sig := hexSHA256(string(body), secret)
	cfg := signatureConfig{Header: "X-Sig", Algorithm: "", Encoding: "hex", Secret: secret}
	if err := verifyHMAC(body, sig, cfg); err != nil {
		t.Fatalf("default-algo verify: %v", err)
	}
}

func TestClassifySigErr(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errEmptyHeader, "missing_header"},
		{errMalformedSignature, "malformed"},
		{errUnknownAlgorithm, "malformed"},
		{errEmptySecret, "config_error"},
		{errSignatureMismatch, "mismatch"},
		{errors.New("anything else"), "mismatch"},
	}
	for _, c := range cases {
		if got := classifySigErr(c.err); got != c.want {
			t.Errorf("classifySigErr(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
