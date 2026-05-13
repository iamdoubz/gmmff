//go:build !js

package localmode

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// CertPaths holds the paths to the generated certificate and key files.
type CertPaths struct {
	CertFile string
	KeyFile  string
}

// GenerateSelfSignedCert generates a self-signed TLS certificate valid for
// all local network interfaces. The certificate is written to os.TempDir()
// as "gmmff-cert.pem" and "gmmff-key.pem". If files already exist they are
// overwritten.
//
// The certificate is valid for 24 hours and covers:
//   - localhost / 127.0.0.1 / ::1
//   - All non-loopback IPv4 and IPv6 addresses on the current machine
//
// Returns the paths and a cleanup function that removes the files.
func GenerateSelfSignedCert() (CertPaths, func(), error) {
	certFile := filepath.Join(os.TempDir(), "gmmff-cert.pem")
	keyFile  := filepath.Join(os.TempDir(), "gmmff-key.pem")

	paths   := CertPaths{CertFile: certFile, KeyFile: keyFile}
	cleanup := func() {
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
	}

	// Generate ECDSA P-256 key pair.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return paths, cleanup, fmt.Errorf("local: generate key: %w", err)
	}

	// Collect all local IP addresses so the cert covers every interface.
	ips := []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				ips = append(ips, ip)
			}
		}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return paths, cleanup, fmt.Errorf("local: serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"gmmff local"},
			CommonName:   "gmmff-local",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute), // small back-date for clock skew
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           ips,
		DNSNames:              []string{"localhost", "gmmff.local"},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return paths, cleanup, fmt.Errorf("local: create certificate: %w", err)
	}

	// Write certificate PEM.
	certOut, err := os.Create(certFile)
	if err != nil {
		return paths, cleanup, fmt.Errorf("local: write cert file: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		_ = certOut.Close()
		return paths, cleanup, fmt.Errorf("local: encode cert: %w", err)
	}
	_ = certOut.Close()

	// Write private key PEM.
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return paths, cleanup, fmt.Errorf("local: marshal key: %w", err)
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return paths, cleanup, fmt.Errorf("local: write key file: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER}); err != nil {
		_ = keyOut.Close()
		return paths, cleanup, fmt.Errorf("local: encode key: %w", err)
	}
	_ = keyOut.Close()

	return paths, cleanup, nil
}
