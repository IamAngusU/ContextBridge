package cluster

import (
	"bytes"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	LANJoinBundleVersion       = 1
	maximumLANJoinBundleBytes  = 256 << 10
	maximumLANCertificateBytes = 128 << 10
)

// RelayTrust is an explicitly transferred LAN relay identity. It is public
// material, but its integrity is security-critical: possession of a modified
// bundle must never silently change which relay a worker trusts.
type RelayTrust struct {
	SPKISHA256     string `json:"spki_sha256"`
	CertificatePEM string `json:"certificate_pem"`
}

type LANJoinBundle struct {
	Version   int        `json:"version"`
	RelayURL  string     `json:"relay_url"`
	Trust     RelayTrust `json:"trust"`
	CreatedAt time.Time  `json:"created_at"`
}

func EnsureLANTLSIdentity(certificatePath, privateKeyPath, advertisedHost string, now time.Time) (RelayTrust, error) {
	certificatePath, privateKeyPath, advertisedHost, err := normalizeLANTLSIdentityInputs(certificatePath, privateKeyPath, advertisedHost)
	if err != nil {
		return RelayTrust{}, err
	}
	certExists, err := regularFileExists(certificatePath, maximumLANCertificateBytes)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("LAN TLS certificate: %w", err)
	}
	keyExists, err := regularFileExists(privateKeyPath, maximumLANCertificateBytes)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("LAN TLS private key: %w", err)
	}
	if certExists != keyExists {
		return RelayTrust{}, errors.New("LAN TLS identity is incomplete; keep or remove both the certificate and private key")
	}
	if certExists {
		return loadRelayTrustAndKey(certificatePath, privateKeyPath, advertisedHost, now)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("generate LAN TLS key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := cryptorand.Int(cryptorand.Reader, serialLimit)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("generate LAN TLS serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ContextBridge LAN Relay"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if address := net.ParseIP(advertisedHost); address != nil {
		template.IPAddresses = []net.IP{address}
	} else {
		template.DNSNames = []string{advertisedHost}
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("create LAN TLS certificate: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("encode LAN TLS private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if err := writeNewPrivateFile(privateKeyPath, privatePEM); err != nil {
		return RelayTrust{}, fmt.Errorf("write LAN TLS private key: %w", err)
	}
	if err := writeNewPrivateFile(certificatePath, certificatePEM); err != nil {
		_ = os.Remove(privateKeyPath)
		return RelayTrust{}, fmt.Errorf("write LAN TLS certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return RelayTrust{}, err
	}
	return RelayTrust{SPKISHA256: relaySPKIFingerprint(certificate), CertificatePEM: string(certificatePEM)}, nil
}

// LoadLANTLSIdentity validates an existing LAN relay identity without creating
// or rotating any trust material. Diagnostics and relay startup must use this
// read-only path; only the explicit LAN initialization command may call Ensure.
func LoadLANTLSIdentity(certificatePath, privateKeyPath, advertisedHost string, now time.Time) (RelayTrust, error) {
	certificatePath, privateKeyPath, advertisedHost, err := normalizeLANTLSIdentityInputs(certificatePath, privateKeyPath, advertisedHost)
	if err != nil {
		return RelayTrust{}, err
	}
	certExists, err := regularFileExists(certificatePath, maximumLANCertificateBytes)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("LAN TLS certificate: %w", err)
	}
	keyExists, err := regularFileExists(privateKeyPath, maximumLANCertificateBytes)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("LAN TLS private key: %w", err)
	}
	if !certExists && !keyExists {
		return RelayTrust{}, errors.New("LAN TLS identity is missing; run `contextbridge cluster lan init` explicitly")
	}
	if certExists != keyExists {
		return RelayTrust{}, errors.New("LAN TLS identity is incomplete; restore the original certificate and private key together")
	}
	return loadRelayTrustAndKey(certificatePath, privateKeyPath, advertisedHost, now)
}

func normalizeLANTLSIdentityInputs(certificatePath, privateKeyPath, advertisedHost string) (string, string, string, error) {
	certificatePath = filepath.Clean(strings.TrimSpace(certificatePath))
	privateKeyPath = filepath.Clean(strings.TrimSpace(privateKeyPath))
	advertisedHost = strings.TrimSpace(advertisedHost)
	if certificatePath == "." || privateKeyPath == "." || advertisedHost == "" {
		return "", "", "", errors.New("LAN TLS certificate, private key, and advertised host are required")
	}
	if sameCleanPath(certificatePath, privateKeyPath) {
		return "", "", "", errors.New("LAN TLS certificate and private key paths must differ")
	}
	return certificatePath, privateKeyPath, advertisedHost, nil
}

func SaveLANJoinBundle(path string, bundle LANJoinBundle) error {
	if err := ValidateLANJoinBundle(bundle, time.Now().UTC()); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if int64(len(raw)) > maximumLANJoinBundleBytes {
		return errors.New("LAN join bundle exceeds its size limit")
	}
	if existing, err := LoadLANJoinBundle(path); err == nil {
		if existing.Version == bundle.Version && existing.RelayURL == bundle.RelayURL && strings.EqualFold(existing.Trust.SPKISHA256, bundle.Trust.SPKISHA256) && existing.Trust.CertificatePEM == bundle.Trust.CertificatePEM {
			return nil
		}
		return errors.New("LAN join bundle already exists with different contents; choose a new --out path")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeNewPrivateFile(path, raw)
}

func LoadLANJoinBundle(path string) (LANJoinBundle, error) {
	raw, err := readBoundedRegularFile(path, maximumLANJoinBundleBytes)
	if err != nil {
		return LANJoinBundle{}, err
	}
	var bundle LANJoinBundle
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return LANJoinBundle{}, fmt.Errorf("parse LAN join bundle: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return LANJoinBundle{}, errors.New("LAN join bundle contains multiple JSON values")
		}
		return LANJoinBundle{}, fmt.Errorf("parse LAN join bundle: %w", err)
	}
	if err := ValidateLANJoinBundle(bundle, time.Now().UTC()); err != nil {
		return LANJoinBundle{}, err
	}
	return bundle, nil
}

func ValidateLANJoinBundle(bundle LANJoinBundle, now time.Time) error {
	if bundle.Version != LANJoinBundleVersion {
		return fmt.Errorf("unsupported LAN join bundle version %d", bundle.Version)
	}
	if err := ValidateRelayURL(bundle.RelayURL); err != nil {
		return fmt.Errorf("LAN relay URL: %w", err)
	}
	parsed, _ := url.Parse(bundle.RelayURL)
	if parsed.Scheme != "https" {
		return errors.New("LAN join bundle relay URL must use HTTPS")
	}
	_, err := relayTLSConfig(bundle.RelayURL, bundle.Trust, now)
	return err
}

func NewRelayHTTPClient(relayURL string, trust RelayTrust, timeout time.Duration) (*http.Client, error) {
	if trust.SPKISHA256 == "" && strings.TrimSpace(trust.CertificatePEM) == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	tlsConfig, err := relayTLSConfig(relayURL, trust, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func relayTLSConfig(relayURL string, trust RelayTrust, now time.Time) (*tls.Config, error) {
	parsed, err := url.Parse(strings.TrimSpace(relayURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errors.New("pinned LAN relay URL must use absolute HTTPS")
	}
	if len(trust.CertificatePEM) == 0 || len(trust.CertificatePEM) > maximumLANCertificateBytes {
		return nil, errors.New("pinned LAN relay certificate is missing or too large")
	}
	block, rest := pem.Decode([]byte(trust.CertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("pinned LAN relay certificate must contain exactly one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("pinned LAN relay certificate is invalid")
	}
	if certificate.IsCA {
		return nil, errors.New("pinned LAN relay identity must be a leaf certificate, not a CA")
	}
	if err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature); err != nil {
		return nil, errors.New("pinned LAN relay certificate must be self-signed")
	}
	if err := certificate.VerifyHostname(parsed.Hostname()); err != nil {
		return nil, errors.New("pinned LAN relay certificate does not cover the relay host")
	}
	if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
		return nil, errors.New("pinned LAN relay certificate is not currently valid")
	}
	want := strings.ToLower(strings.TrimSpace(trust.SPKISHA256))
	got := relaySPKIFingerprint(certificate)
	if want == "" || !strings.HasPrefix(want, "sha256:") || len(strings.TrimPrefix(want, "sha256:")) != 64 {
		return nil, errors.New("pinned LAN relay SPKI fingerprint is invalid")
	}
	if !strings.EqualFold(want, got) {
		return nil, errors.New("pinned LAN relay certificate does not match its SPKI fingerprint")
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return &tls.Config{ // #nosec G402 -- TLS 1.3 and an explicit leaf trust anchor are required.
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		ServerName: parsed.Hostname(),
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || !strings.EqualFold(relaySPKIFingerprint(state.PeerCertificates[0]), want) {
				return errors.New("LAN relay TLS identity does not match the pinned SPKI fingerprint")
			}
			return nil
		},
	}, nil
}

func relaySPKIFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func loadRelayTrustAndKey(certificatePath, privateKeyPath, advertisedHost string, now time.Time) (RelayTrust, error) {
	certificatePEM, err := readLANIdentityFile(certificatePath, maximumLANCertificateBytes, false)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("LAN TLS certificate: %w", err)
	}
	privateKeyPEM, err := readLANIdentityFile(privateKeyPath, maximumLANCertificateBytes, true)
	if err != nil {
		return RelayTrust{}, fmt.Errorf("LAN TLS private key: %w", err)
	}
	pair, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return RelayTrust{}, errors.New("LAN TLS certificate and private key do not match")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return RelayTrust{}, errors.New("LAN TLS certificate is invalid")
	}
	trust := RelayTrust{SPKISHA256: relaySPKIFingerprint(certificate), CertificatePEM: string(certificatePEM)}
	if _, err := relayTLSConfig("https://"+net.JoinHostPort(advertisedHost, "443"), trust, now); err != nil {
		return RelayTrust{}, err
	}
	return trust, nil
}

func readLANIdentityFile(path string, maximum int64, secret bool) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() <= 0 || before.Size() > maximum {
		return nil, errors.New("path must be a bounded regular non-symlink file")
	}
	if secret && runtime.GOOS != "windows" && before.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("private key permissions %04o are too broad; require owner-only access (0600)", before.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Size() <= 0 || after.Size() > maximum {
		return nil, errors.New("identity file changed while it was being validated")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, fmt.Errorf("identity file exceeds %d bytes", maximum)
	}
	return raw, nil
}

func regularFileExists(path string, maximum int64) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum {
		return false, errors.New("path must be a bounded regular non-symlink file")
	}
	return true, nil
}

func writeNewPrivateFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func sameCleanPath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
