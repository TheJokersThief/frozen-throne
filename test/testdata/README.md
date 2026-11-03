# Test data

`app-key.pem` is a throwaway 2048-bit RSA private key used only by the test
harness (mock GitHub App identity). It protects nothing, was generated for
this repository, and must never be used for a real GitHub App.

Regenerate with `openssl genrsa -traditional 2048 > app-key.pem`.
