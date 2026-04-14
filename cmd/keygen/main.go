// Command keygen generates an RSA keypair and prints it as PEM to stdout.
//
// Usage:
//
//	go run ./cmd/keygen [-bits 2048]
//	go run ./cmd/keygen > keypair.pem
//
// Output format:
//
//	-----BEGIN PRIVATE KEY-----   (PKCS#8)
//	...
//	-----END PRIVATE KEY-----
//	---
//	-----BEGIN PUBLIC KEY-----    (PKIX)
//	...
//	-----END PUBLIC KEY-----
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
)

const (
	minBits = 2048
	maxBits = 4096
)

func main() {
	bits := flag.Int("bits", 2048, "RSA key size in bits (2048–4096)")
	flag.Parse()

	if *bits < minBits {
		fmt.Fprintf(os.Stderr, "error: -bits must be at least %d (got %d)\n", minBits, *bits)
		os.Exit(1)
	}
	if *bits > maxBits {
		fmt.Fprintf(os.Stderr, "error: -bits must be at most %d (got %d)\n", maxBits, *bits)
		os.Exit(1)
	}

	key, err := rsa.GenerateKey(rand.Reader, *bits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to generate RSA key: %v\n", err)
		os.Exit(1)
	}

	// Encode private key as PKCS#8 PEM.
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to marshal private key: %v\n", err)
		os.Exit(1)
	}
	privBlock := &pem.Block{Type: "PRIVATE KEY", Bytes: privDER}

	// Encode public key as PKIX PEM.
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to marshal public key: %v\n", err)
		os.Exit(1)
	}
	pubBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}

	if err := pem.Encode(os.Stdout, privBlock); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("---")
	if err := pem.Encode(os.Stdout, pubBlock); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
