package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mobile-use/internal/wire"
)

func pki(args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		fmt.Println("mobile-use pki init --dir PATH\nmobile-use pki issue --dir CA_PATH --out PATH --name NAME --usage client|server|both [--hosts HOST,IP]")
		return nil
	}
	f := flag.NewFlagSet("pki "+args[0], flag.ContinueOnError)
	dir := f.String("dir", "", "Private CA directory")
	out := f.String("out", "", "Destination for the issued certificate and key")
	name := f.String("name", "", "Certificate common name")
	usage := f.String("usage", "client", "Extended usage for issued certificate")
	hosts := f.String("hosts", "", "Comma-separated DNS/IP SANs for the server certificate")
	days := f.Int("days", 30, "Leaf certificate lifetime in days (1–90)")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *dir == "" || f.NArg() != 0 {
		return fmt.Errorf("provide a CA directory")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	certificate := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Minute), BasicConstraintsValid: true}
	var parent *x509.Certificate
	var signer *ecdsa.PrivateKey
	var destination string
	switch args[0] {
	case "init":
		certificate.Subject = pkix.Name{CommonName: "mobile-use private CA"}
		certificate.IsCA = true
		certificate.MaxPathLenZero = true
		certificate.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		certificate.NotAfter = now.AddDate(4, 0, 0)
		parent, signer, destination = certificate, key, filepath.Join(*dir, "ca")
	case "issue":
		if !wire.ValidID(*name) || *out == "" || *days < 1 || *days > 90 {
			return fmt.Errorf("set a valid name, output path, and lifetime")
		}
		parent, signer, err = readCA(*dir)
		if err != nil {
			return err
		}
		certificate.Subject = pkix.Name{CommonName: *name}
		certificate.NotAfter = now.AddDate(0, 0, *days)
		if certificate.NotAfter.After(parent.NotAfter) {
			return fmt.Errorf("leaf certificate would outlive the CA")
		}
		certificate.KeyUsage = x509.KeyUsageDigitalSignature
		switch *usage {
		case "client":
			certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		case "server", "both":
			certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			if *usage == "both" {
				certificate.ExtKeyUsage = append(certificate.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
			}
			for _, host := range strings.Split(*hosts, ",") {
				if ip := net.ParseIP(host); ip != nil {
					certificate.IPAddresses = append(certificate.IPAddresses, ip)
					continue
				}
				if host != "" && !strings.ContainsAny(host, " /@*:") {
					certificate.DNSNames = append(certificate.DNSNames, host)
				}
			}
			if len(certificate.IPAddresses)+len(certificate.DNSNames) == 0 {
				return fmt.Errorf("server certificates require --hosts")
			}
		default:
			return fmt.Errorf("invalid certificate usage")
		}
		destination = filepath.Join(*out, *name)
	default:
		return fmt.Errorf("usage: mobile-use pki init|issue")
	}
	encoded, err := x509.CreateCertificate(rand.Reader, certificate, parent, &key.PublicKey, signer)
	if err != nil {
		return err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	for _, suffix := range []string{".crt", ".key"} {
		if _, err := os.Lstat(destination + suffix); err == nil {
			return fmt.Errorf("certificate destination already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := writeNew(destination+".key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		return err
	}
	if err := writeNew(destination+".crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0644); err != nil {
		return err
	}
	fmt.Println("Issued", destination+".crt")
	return nil
}

func readCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, nil, fmt.Errorf("invalid CA PEM")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if !certificate.IsCA || !time.Now().Before(certificate.NotAfter) {
		return nil, nil, fmt.Errorf("the configured CA is invalid or expired")
	}
	private, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := private.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("the CA key must use ECDSA")
	}
	return certificate, key, nil
}
