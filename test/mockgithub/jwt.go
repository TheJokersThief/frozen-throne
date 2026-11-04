package mockgithub

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// verifyAppJWT checks an RS256 App JWT: signature, issuer and expiry.
func verifyAppJWT(raw string, appID int64, pub *rsa.PublicKey, now time.Time) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return errors.New("malformed jwt")
	}
	enc := base64.RawURLEncoding
	hdrB, err := enc.DecodeString(parts[0])
	if err != nil {
		return errors.New("malformed jwt header")
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(hdrB, &hdr) != nil || hdr.Alg != "RS256" {
		return errors.New("unsupported jwt alg")
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return errors.New("malformed jwt signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return errors.New("bad jwt signature")
	}
	claimsB, err := enc.DecodeString(parts[1])
	if err != nil {
		return errors.New("malformed jwt claims")
	}
	var c struct {
		Iss json.Number `json:"iss"`
		Exp int64       `json:"exp"`
		Iat int64       `json:"iat"`
	}
	if json.Unmarshal(claimsB, &c) != nil {
		return errors.New("malformed jwt claims")
	}
	if c.Iss.String() != strconv.FormatInt(appID, 10) {
		return errors.New("jwt issuer is not this app")
	}
	if now.Unix() >= c.Exp {
		return errors.New("jwt expired")
	}
	if c.Exp-c.Iat > 11*60 {
		return errors.New("jwt lifetime exceeds 10 minutes")
	}
	return nil
}
