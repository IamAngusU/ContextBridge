package cluster

import (
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	PoolAuthorityV1              = "contextbridge.pool-authority.v1"
	PoolWorkerCertificateV1      = "contextbridge.pool-worker.v1"
	PoolJobAuthorizationV1       = "contextbridge.pool-job.v1"
	maximumPoolAuthorityFileSize = 16 << 10
	poolJobAuthorizationLifetime = 24 * time.Hour
)

// PoolAuthority is customer-held signing material. The private key belongs on
// producer systems only; relays need neither the file nor its contents.
type PoolAuthority struct {
	ContractVersion string    `json:"contract_version"`
	PoolID          string    `json:"pool_id"`
	PublicKey       string    `json:"public_key"`
	PrivateKey      string    `json:"private_key"`
	CreatedAt       time.Time `json:"created_at"`
}

// PoolWorkerCertificate proves that the customer authority admitted one exact
// worker encryption key. Relays may carry this public certificate but cannot
// create a certificate for a key they control.
type PoolWorkerCertificate struct {
	ContractVersion string    `json:"contract_version"`
	PoolID          string    `json:"pool_id"`
	AuthorityKey    string    `json:"authority_public_key"`
	WorkerPublicKey string    `json:"worker_public_key"`
	IssuedAt        time.Time `json:"issued_at"`
	Signature       string    `json:"signature"`
}

// PoolJobAuthorization proves that the customer approved the exact encrypted
// bytes and execution context delivered to a certified worker.
type PoolJobAuthorization struct {
	ContractVersion string    `json:"contract_version"`
	PoolID          string    `json:"pool_id"`
	ExpiresAt       time.Time `json:"expires_at"`
	Signature       string    `json:"signature"`
}

type poolWorkerCertificateClaims struct {
	ContractVersion string    `json:"contract_version"`
	PoolID          string    `json:"pool_id"`
	AuthorityKey    string    `json:"authority_public_key"`
	WorkerPublicKey string    `json:"worker_public_key"`
	IssuedAt        time.Time `json:"issued_at"`
}

type poolJobAuthorizationClaims struct {
	ContractVersion string            `json:"contract_version"`
	PoolID          string            `json:"pool_id"`
	ExpiresAt       time.Time         `json:"expires_at"`
	Context         EncryptionContext `json:"context"`
	SealedSHA256    string            `json:"sealed_sha256"`
}

func NewPoolAuthority(poolID string, now time.Time) (PoolAuthority, error) {
	poolID = strings.TrimSpace(poolID)
	if !validRoutingLabel(poolID, 120) {
		return PoolAuthority{}, errors.New("pool ID must use 1 to 120 safe UTF-8 bytes")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		return PoolAuthority{}, fmt.Errorf("generate pool authority: %w", err)
	}
	return PoolAuthority{
		ContractVersion: PoolAuthorityV1,
		PoolID:          poolID,
		PublicKey:       base64.StdEncoding.EncodeToString(publicKey),
		PrivateKey:      base64.StdEncoding.EncodeToString(privateKey),
		CreatedAt:       now.UTC(),
	}, nil
}

// CreatePoolAuthorityFile creates a new owner-only file and never overwrites an
// existing authority. Losing or replacing this key intentionally invalidates
// the ability to authorize work for workers certified by it.
func CreatePoolAuthorityFile(path, poolID string, now time.Time) (PoolAuthority, error) {
	authority, err := NewPoolAuthority(poolID, now)
	if err != nil {
		return PoolAuthority{}, err
	}
	// #nosec G117 -- this command intentionally writes the generated private key once to a new owner-only file and never returns it over the network.
	raw, err := json.MarshalIndent(authority, "", "  ")
	if err != nil {
		return PoolAuthority{}, err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return PoolAuthority{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return PoolAuthority{}, err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return PoolAuthority{}, err
	}
	if err := file.Sync(); err != nil {
		return PoolAuthority{}, err
	}
	if err := file.Close(); err != nil {
		return PoolAuthority{}, err
	}
	committed = true
	return authority, nil
}

func LoadPoolAuthority(path string) (PoolAuthority, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return PoolAuthority{}, errors.New("pool authority file is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return PoolAuthority{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return PoolAuthority{}, errors.New("pool authority must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > maximumPoolAuthorityFileSize {
		return PoolAuthority{}, fmt.Errorf("pool authority file must contain 1 to %d bytes", maximumPoolAuthorityFileSize)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return PoolAuthority{}, errors.New("pool authority file permissions must deny group and other access")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return PoolAuthority{}, err
	}
	var authority PoolAuthority
	if err := json.Unmarshal(raw, &authority); err != nil {
		return PoolAuthority{}, errors.New("pool authority file is invalid")
	}
	if err := validatePoolAuthority(authority); err != nil {
		return PoolAuthority{}, err
	}
	return authority, nil
}

func validatePoolAuthority(authority PoolAuthority) error {
	if authority.ContractVersion != PoolAuthorityV1 || !validRoutingLabel(authority.PoolID, 120) || authority.CreatedAt.IsZero() {
		return errors.New("pool authority metadata is invalid")
	}
	publicKey, err := decodeEd25519PublicKey(authority.PublicKey)
	if err != nil {
		return errors.New("pool authority public key is invalid")
	}
	privateRaw, err := base64.StdEncoding.DecodeString(authority.PrivateKey)
	if err != nil || len(privateRaw) != ed25519.PrivateKeySize || base64.StdEncoding.EncodeToString(privateRaw) != authority.PrivateKey {
		return errors.New("pool authority private key is invalid")
	}
	privateKey := ed25519.PrivateKey(privateRaw)
	if !privateKey.Public().(ed25519.PublicKey).Equal(publicKey) {
		return errors.New("pool authority public and private keys do not match")
	}
	return nil
}

func CertifyPoolWorker(authority PoolAuthority, workerPublicKey string, now time.Time) (*PoolWorkerCertificate, error) {
	if err := validatePoolAuthority(authority); err != nil {
		return nil, err
	}
	if _, err := parsePublicKey(workerPublicKey); err != nil {
		return nil, errors.New("worker encryption key is invalid")
	}
	certificate := &PoolWorkerCertificate{
		ContractVersion: PoolWorkerCertificateV1,
		PoolID:          authority.PoolID,
		AuthorityKey:    authority.PublicKey,
		WorkerPublicKey: workerPublicKey,
		IssuedAt:        now.UTC(),
	}
	claims := poolWorkerClaims(*certificate)
	encoded, _ := json.Marshal(claims)
	privateRaw, _ := base64.StdEncoding.DecodeString(authority.PrivateKey)
	certificate.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateRaw), encoded))
	return certificate, nil
}

func ValidatePoolWorkerCertificate(certificate *PoolWorkerCertificate, trustedAuthorityKey, poolID, workerPublicKey string, now time.Time) error {
	if certificate == nil {
		return errors.New("selected worker has no customer pool certificate")
	}
	if certificate.ContractVersion != PoolWorkerCertificateV1 || certificate.PoolID != poolID || certificate.AuthorityKey != trustedAuthorityKey || certificate.WorkerPublicKey != workerPublicKey {
		return errors.New("selected worker is not certified for the configured customer pool")
	}
	if certificate.IssuedAt.IsZero() || certificate.IssuedAt.After(now.UTC().Add(5*time.Minute)) {
		return errors.New("selected worker certificate has an invalid issue time")
	}
	publicKey, err := decodeEd25519PublicKey(certificate.AuthorityKey)
	if err != nil {
		return errors.New("selected worker certificate has an invalid authority key")
	}
	signature, err := decodeEd25519Signature(certificate.Signature)
	if err != nil {
		return errors.New("selected worker certificate has an invalid signature")
	}
	encoded, _ := json.Marshal(poolWorkerClaims(*certificate))
	if !ed25519.Verify(publicKey, encoded, signature) {
		return errors.New("selected worker certificate signature is invalid")
	}
	return nil
}

func ValidateAssignmentPoolAuthority(authority PoolAuthority, response AssignmentResponse, now time.Time) error {
	if err := validatePoolAuthority(authority); err != nil {
		return err
	}
	return ValidatePoolWorkerCertificate(response.Assignment.PoolCertificate, authority.PublicKey, authority.PoolID, response.Assignment.PublicKey, now)
}

func (authority PoolAuthority) BindAssignmentRequest(request *AssignmentRequest) error {
	if request == nil {
		return errors.New("assignment request is required")
	}
	if err := validatePoolAuthority(authority); err != nil {
		return err
	}
	request.PoolID = authority.PoolID
	request.PoolAuthorityKey = authority.PublicKey
	return nil
}

func validatePoolAssignmentSelector(poolID, authorityKey string) error {
	if (poolID == "") != (authorityKey == "") {
		return errors.New("pool_id and pool_authority_public_key must be supplied together")
	}
	if poolID == "" {
		return nil
	}
	if !validRoutingLabel(poolID, 120) {
		return errors.New("pool_id must use 1 to 120 safe UTF-8 bytes")
	}
	if _, err := decodeEd25519PublicKey(authorityKey); err != nil {
		return errors.New("pool_authority_public_key is invalid")
	}
	return nil
}

func nodesForPoolAssignment(nodes []Node, poolID, authorityKey string, now time.Time) []Node {
	selected := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		certificate := node.PoolCertificate
		if poolID == "" {
			if certificate == nil {
				selected = append(selected, node)
			}
			continue
		}
		if certificate == nil || certificate.PoolID != poolID || certificate.AuthorityKey != authorityKey {
			continue
		}
		if ValidatePoolWorkerCertificate(certificate, authorityKey, poolID, node.PublicKey, now) == nil {
			selected = append(selected, node)
		}
	}
	return selected
}

func SignPoolJobAuthorization(authority PoolAuthority, context EncryptionContext, sealed *SealedEnvelope, now time.Time) (*PoolJobAuthorization, error) {
	if err := validatePoolAuthority(authority); err != nil {
		return nil, err
	}
	if err := context.Validate(); err != nil {
		return nil, err
	}
	if sealed == nil {
		return nil, errors.New("customer pool authorization requires an encrypted payload")
	}
	authorization := &PoolJobAuthorization{
		ContractVersion: PoolJobAuthorizationV1,
		PoolID:          authority.PoolID,
		ExpiresAt:       now.UTC().Add(poolJobAuthorizationLifetime),
	}
	claims, err := poolJobClaims(*authorization, context, sealed)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(claims)
	privateRaw, _ := base64.StdEncoding.DecodeString(authority.PrivateKey)
	authorization.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateRaw), encoded))
	return authorization, nil
}

func ValidatePoolJobAuthorization(authorityKey, poolID string, authorization *PoolJobAuthorization, context EncryptionContext, sealed *SealedEnvelope, now time.Time) error {
	if authorization == nil {
		return errors.New("customer pool authorization is required")
	}
	if authorization.ContractVersion != PoolJobAuthorizationV1 || authorization.PoolID != poolID {
		return errors.New("customer pool authorization targets another pool")
	}
	if sealed == nil {
		return errors.New("customer pool accepts only encrypted jobs")
	}
	if !authorization.ExpiresAt.After(now.UTC()) || authorization.ExpiresAt.After(now.UTC().Add(poolJobAuthorizationLifetime+5*time.Minute)) {
		return errors.New("customer pool authorization is expired or unreasonably long")
	}
	publicKey, err := decodeEd25519PublicKey(authorityKey)
	if err != nil {
		return errors.New("customer pool authority key is invalid")
	}
	signature, err := decodeEd25519Signature(authorization.Signature)
	if err != nil {
		return errors.New("customer pool authorization signature is invalid")
	}
	claims, err := poolJobClaims(*authorization, context, sealed)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(claims)
	if !ed25519.Verify(publicKey, encoded, signature) {
		return errors.New("customer pool authorization does not match this encrypted job")
	}
	return nil
}

func poolWorkerClaims(certificate PoolWorkerCertificate) poolWorkerCertificateClaims {
	return poolWorkerCertificateClaims{
		ContractVersion: certificate.ContractVersion,
		PoolID:          certificate.PoolID,
		AuthorityKey:    certificate.AuthorityKey,
		WorkerPublicKey: certificate.WorkerPublicKey,
		IssuedAt:        certificate.IssuedAt.UTC(),
	}
}

func poolJobClaims(authorization PoolJobAuthorization, context EncryptionContext, sealed *SealedEnvelope) (poolJobAuthorizationClaims, error) {
	encodedSealed, err := json.Marshal(sealed)
	if err != nil {
		return poolJobAuthorizationClaims{}, err
	}
	digest := sha256.Sum256(encodedSealed)
	return poolJobAuthorizationClaims{
		ContractVersion: authorization.ContractVersion,
		PoolID:          authorization.PoolID,
		ExpiresAt:       authorization.ExpiresAt.UTC(),
		Context:         context,
		SealedSHA256:    fmt.Sprintf("%x", digest[:]),
	}, nil
}

func decodeEd25519PublicKey(value string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(raw) != value {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

func decodeEd25519Signature(value string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(raw) != value {
		return nil, errors.New("invalid Ed25519 signature")
	}
	return raw, nil
}
