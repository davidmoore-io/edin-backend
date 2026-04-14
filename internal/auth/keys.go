package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// ErrInvalidPEM is returned when PEM decoding fails.
var ErrInvalidPEM = errors.New("auth: invalid PEM data")

// ErrNotRSAKey is returned when a PEM block decodes to a non-RSA key.
var ErrNotRSAKey = errors.New("auth: key is not an RSA key")

// ErrKeyTooSmall is returned when an RSA key is smaller than MinKeyBits.
var ErrKeyTooSmall = errors.New("auth: RSA key must be at least 2048 bits")

// MinKeyBits is the minimum acceptable RSA key size in bits.
const MinKeyBits = 2048

// LoadPrivateKey parses a PEM-encoded RSA private key from a byte slice.
// It accepts both PKCS#8 ("PRIVATE KEY") and PKCS#1 ("RSA PRIVATE KEY") PEM blocks.
// Returns ErrInvalidPEM if the PEM cannot be decoded.
// Returns ErrNotRSAKey if the key is not RSA.
// Returns ErrKeyTooSmall if the key is < MinKeyBits bits.
func LoadPrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, ErrInvalidPEM
	}

	var rsaKey *rsa.PrivateKey

	switch block.Type {
	case "PRIVATE KEY":
		// PKCS#8
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.Join(ErrInvalidPEM, err)
		}
		var ok bool
		rsaKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, ErrNotRSAKey
		}
	case "RSA PRIVATE KEY":
		// PKCS#1
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.Join(ErrInvalidPEM, err)
		}
		rsaKey = key
	default:
		return nil, ErrNotRSAKey
	}

	if rsaKey.N.BitLen() < MinKeyBits {
		return nil, ErrKeyTooSmall
	}

	return rsaKey, nil
}

// LoadPublicKey parses a PEM-encoded RSA public key from a byte slice.
// Accepts PKIX/SubjectPublicKeyInfo format ("PUBLIC KEY" PEM block).
// Returns ErrInvalidPEM if the PEM cannot be decoded.
// Returns ErrNotRSAKey if the key is not RSA.
func LoadPublicKey(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, ErrInvalidPEM
	}

	if block.Type != "PUBLIC KEY" {
		return nil, ErrInvalidPEM
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.Join(ErrInvalidPEM, err)
	}

	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, ErrNotRSAKey
	}

	return rsaKey, nil
}
