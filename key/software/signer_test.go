package software_test

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/truvity/atoom/key/purpose"
	"github.com/truvity/atoom/key/signer"
	"github.com/truvity/atoom/key/software"
)

// A certificate signed through key/signer over the software driver verifies: the driver's own signature, DER for an EC key and plain
// PKCS#1 v1.5 for an RSA key, passes through the Signer to crypto/x509 as it is.
func TestSignerOverTheDriverMakesACertificateThatVerifies(t *testing.T) {
	want := map[string]x509.SignatureAlgorithm{
		purpose.ES256: x509.ECDSAWithSHA256,
		purpose.ES384: x509.ECDSAWithSHA384,
		purpose.RS256: x509.SHA256WithRSA,
	}
	for alg, sigAlg := range want {
		t.Run(alg, func(t *testing.T) {
			decls := []purpose.Declaration{{Name: "root-signer", Algorithm: alg, Operations: []purpose.Operation{purpose.Sign}}}
			r := startWith(t, software.Options{Purposes: decls})
			ensure(t, r.client, "root-signer", alg)
			s, err := signer.New(t.Context(), r.client, "root-signer")
			if err != nil {
				t.Fatalf("signer.New: %v", err)
			}
			tmpl := &x509.Certificate{
				SerialNumber:          big.NewInt(1),
				Subject:               pkix.Name{CommonName: "signer over the driver"},
				NotBefore:             time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
				NotAfter:              time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC),
				IsCA:                  true,
				BasicConstraintsValid: true,
				KeyUsage:              x509.KeyUsageCertSign,
			}
			der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, s.Public(), s)
			if err != nil {
				t.Fatalf("CreateCertificate: %v", err)
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			if cert.SignatureAlgorithm != sigAlg {
				t.Errorf("signature algorithm %v, want %v", cert.SignatureAlgorithm, sigAlg)
			}
			if err := cert.CheckSignatureFrom(cert); err != nil {
				t.Fatalf("CheckSignatureFrom: %v", err)
			}
		})
	}
}
