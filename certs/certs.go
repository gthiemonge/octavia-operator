package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/openstack-k8s-operators/octavia-operator/pkg/octavia"
)

var (
	subjectDefault = pkix.Name{
		CommonName:         "www.example.com",
		Organization:       []string{"OpenStack"},
		OrganizationalUnit: []string{"Octavia Amphorae"},
		Country:            []string{"DE"},
		Province:           []string{"Bavaria"},
		Locality:           []string{"Piding"},
	}
)

// generateKey generates a PEM encoded private RSA key and applies PEM
// encryption if given passphrase is not an empty string.
func generateKey(passphrase []byte) (*rsa.PrivateKey, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}
	pkcs8Key, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		err = fmt.Errorf("Error private key to PKCS #8 form: %w", err)
		return priv, nil, err
	}

	var pemBlock *pem.Block
	if passphrase != nil {
		pemBlock, err = octavia.EncryptPrivateKey(pkcs8Key, passphrase)
		if err != nil {
			err = fmt.Errorf("Error encrypting private key: %w", err)
			return priv, nil, err
		}
	} else {
		pemBlock = &pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Key}
	}

	privPEM := new(bytes.Buffer)
	err = pem.Encode(privPEM, pemBlock)
	if err != nil {
		return priv, nil, err
	}

	return priv, privPEM.Bytes(), nil
}

func generateCACert(caPrivKey *rsa.PrivateKey, commonName string) ([]byte, *x509.Certificate, error) {
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2019),
		Subject:               subjectDefault,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign | x509.KeyUsageCertSign,
	}
	caTemplate.Subject.CommonName = commonName

	caBytes, err := x509.CreateCertificate(
		rand.Reader, caTemplate, caTemplate, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, nil, err
	}
	caCertPEM := new(bytes.Buffer)
	err = pem.Encode(caCertPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	if err != nil {
		return nil, nil, err
	}
	return caCertPEM.Bytes(), caTemplate, nil
}

// Create a certificate and key for the client and sign it with the CA
func generateClientCert(caTemplate *x509.Certificate, certPrivKey *rsa.PrivateKey, caPrivKey *rsa.PrivateKey, commonName string) ([]byte, error) {

	certTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2019),
		Subject:               subjectDefault,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  false,
		BasicConstraintsValid: false,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageEmailProtection},
	}
	certTemplate.Subject.CommonName = commonName

	certBytes, err := x509.CreateCertificate(
		rand.Reader, certTemplate, caTemplate, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, err
	}

	certPEM := new(bytes.Buffer)
	err = pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})
	if err != nil {
		return nil, err
	}

	return certPEM.Bytes(), nil
}

func generateClientCerts() error {
	clientCAKey, _, err := generateKey(nil)
	if err != nil {
		return fmt.Errorf("Error while generating client CA key: %w", err)
	}
	clientCACert, clientCATemplate, err := generateCACert(clientCAKey, "Octavia client CA")
	if err != nil {
		return fmt.Errorf("Error while generating amphora client CA certificate: %w", err)
	}

	clientKey, clientKeyPEM, err := generateKey(nil)
	if err != nil {
		return fmt.Errorf("Error while generating amphora client key: %w", err)
	}
	clientCert, err := generateClientCert(clientCATemplate, clientKey, clientCAKey, "Octavia controller")
	if err != nil {
		return fmt.Errorf("Error while generating amphora client certificate: %w", err)
	}
	clientKeyAndCert := append(clientKeyPEM, clientCert...)

	err = os.WriteFile("new-client_ca.cert.pem", clientCACert, 0644)
	if err != nil {
		return err
	}

	err = os.WriteFile("new-client.cert-and-key.pem", clientKeyAndCert, 0644)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	err := generateClientCerts()
	if err != nil {
		fmt.Printf("%s\n", err)
	}
}
