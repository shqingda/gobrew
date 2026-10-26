package catalog

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// Official Homebrew key, from Library/Homebrew/api/homebrew-1.pem at
// https://github.com/Homebrew/brew (BSD-2-Clause; see HOMEBREW-LICENSE.txt).
// Key rotation deliberately requires a reviewed application update.
//
//go:embed homebrew-1.pem
var officialKeyPEM []byte

func officialKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode(officialKeyPEM)
	if block == nil {
		return nil, errors.New("embedded Homebrew key is invalid")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("Homebrew key is not RSA")
	}
	return rsaKey, nil
}

// verify checks Homebrew's detached, unencoded-payload JWS using a pinned key.
// No algorithm or remote key advertised by the response can select a verifier.
func verify(data []byte, key *rsa.PublicKey) ([]byte, error) {
	var envelope struct {
		Payload    *string `json:"payload"`
		Signatures []struct {
			Header struct {
				KeyID string `json:"kid"`
			} `json:"header"`
			Protected string `json:"protected"`
			Signature string `json:"signature"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode signed catalog: %w", err)
	}
	if envelope.Payload == nil {
		return nil, errors.New("signed catalog has no payload")
	}
	for _, sig := range envelope.Signatures {
		if sig.Header.KeyID != "homebrew-1" {
			continue
		}
		protected, err := decodeBase64URL(sig.Protected)
		if err != nil {
			return nil, fmt.Errorf("JWS protected header: %w", err)
		}
		var header struct {
			Algorithm string   `json:"alg"`
			Encoded   *bool    `json:"b64"`
			Critical  []string `json:"crit"`
		}
		if err = json.Unmarshal(protected, &header); err != nil {
			return nil, fmt.Errorf("JWS header: %w", err)
		}
		if header.Algorithm != "PS512" || header.Encoded == nil || *header.Encoded {
			return nil, errors.New("unsupported catalog signature algorithm or encoding")
		}
		for _, name := range header.Critical {
			if name != "b64" {
				return nil, fmt.Errorf("unknown critical JWS header %q", name)
			}
		}
		bytes, err := decodeBase64URL(sig.Signature)
		if err != nil {
			return nil, fmt.Errorf("decode catalog signature: %w", err)
		}
		hash := sha512.New()
		_, _ = hash.Write([]byte(sig.Protected))
		_, _ = hash.Write([]byte("."))
		_, _ = hash.Write([]byte(*envelope.Payload))
		if err = rsa.VerifyPSS(key, crypto.SHA512, hash.Sum(nil), bytes, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
			return nil, fmt.Errorf("catalog signature verification failed: %w", err)
		}
		return []byte(*envelope.Payload), nil
	}
	return nil, errors.New("catalog has no signature by the trusted Homebrew key")
}

func decodeBase64URL(value string) ([]byte, error) {
	if strings.HasSuffix(value, "=") {
		return base64.URLEncoding.DecodeString(value)
	}
	return base64.RawURLEncoding.DecodeString(value)
}
