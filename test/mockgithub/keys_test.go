package mockgithub_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func TestParsePublicKeyAcceptsPrivateAndPublicPEM(t *testing.T) {
	key := loadKey(t)
	priv, err := os.ReadFile("../testdata/app-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pkix := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})

	for name, in := range map[string][]byte{"private": priv, "pkix": pkix, "pkcs1": pkcs1} {
		got, err := mockgithub.ParsePublicKey(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.Equal(&key.PublicKey) {
			t.Fatalf("%s: key mismatch", name)
		}
	}
	if _, err := mockgithub.ParsePublicKey([]byte("junk")); err == nil {
		t.Fatal("want error for junk")
	}
}
