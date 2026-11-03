package mockgithub

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// ParsePublicKey returns the RSA public key in pemBytes. It accepts PKIX
// ("PUBLIC KEY"), PKCS#1 ("RSA PUBLIC KEY") and, for convenience, private
// keys (PKCS#1 or PKCS#8), from which the public half is derived.
func ParsePublicKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("mockgithub: key is not PEM encoded")
	}
	if k, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if pub, ok := k.(*rsa.PublicKey); ok {
			return pub, nil
		}
		return nil, errors.New("mockgithub: public key is not RSA")
	}
	if pub, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return pub, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return &k.PublicKey, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mockgithub: unrecognised key: %w", err)
	}
	if rk, ok := k.(*rsa.PrivateKey); ok {
		return &rk.PublicKey, nil
	}
	return nil, errors.New("mockgithub: key is not RSA")
}
