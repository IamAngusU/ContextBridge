package cluster

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const sealedAlgorithm = "X25519-AES-256-GCM"

func NewIdentity() (privateKey, publicKey string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return encode(key.Bytes()), encode(key.PublicKey().Bytes()), nil
}

func SealFor(publicKey string, plaintext, aad []byte) (*SealedEnvelope, string, error) {
	recipient, err := parsePublicKey(publicKey)
	if err != nil {
		return nil, "", err
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, "", err
	}
	key := deriveKey(shared, []byte("contextbridge-job-v1"))
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", err
	}
	// #nosec G407 -- nonce is filled from crypto/rand immediately above.
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	return &SealedEnvelope{
		Algorithm: sealedAlgorithm, EphemeralPublic: encode(ephemeral.PublicKey().Bytes()),
		Nonce: encode(nonce), Ciphertext: encode(ciphertext),
	}, encode(shared), nil
}

func OpenWith(privateKey string, envelope *SealedEnvelope, aad []byte) ([]byte, string, error) {
	if envelope == nil || envelope.Algorithm != sealedAlgorithm {
		return nil, "", errors.New("unsupported or missing sealed envelope")
	}
	privateRaw, err := decode(privateKey)
	if err != nil {
		return nil, "", fmt.Errorf("decode private key: %w", err)
	}
	private, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, "", err
	}
	sender, err := parsePublicKey(envelope.EphemeralPublic)
	if err != nil {
		return nil, "", err
	}
	shared, err := private.ECDH(sender)
	if err != nil {
		return nil, "", err
	}
	plaintext, err := openShared(shared, envelope, aad, "contextbridge-job-v1")
	return plaintext, encode(shared), err
}

func SealResponse(sharedKey string, plaintext, aad []byte) (*SealedEnvelope, error) {
	shared, err := decode(sharedKey)
	if err != nil {
		return nil, err
	}
	key := deriveKey(shared, []byte("contextbridge-result-v1"))
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// #nosec G407 -- nonce is filled from crypto/rand immediately above.
	return &SealedEnvelope{Algorithm: sealedAlgorithm, Nonce: encode(nonce), Ciphertext: encode(gcm.Seal(nil, nonce, plaintext, aad))}, nil
}

func OpenResponse(sharedKey string, envelope *SealedEnvelope, aad []byte) ([]byte, error) {
	shared, err := decode(sharedKey)
	if err != nil {
		return nil, err
	}
	return openShared(shared, envelope, aad, "contextbridge-result-v1")
}

func openShared(shared []byte, envelope *SealedEnvelope, aad []byte, purpose string) ([]byte, error) {
	if envelope == nil || envelope.Algorithm != sealedAlgorithm {
		return nil, errors.New("unsupported or missing sealed envelope")
	}
	block, err := aes.NewCipher(deriveKey(shared, []byte(purpose)))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := decode(envelope.Nonce)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("sealed envelope nonce has an invalid length")
	}
	ciphertext, err := decode(envelope.Ciphertext)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func deriveKey(secret, info []byte) []byte {
	extract := hmac.New(sha256.New, make([]byte, sha256.Size))
	extract.Write(secret)
	prk := extract.Sum(nil)
	expand := hmac.New(sha256.New, prk)
	expand.Write(info)
	expand.Write([]byte{1})
	return expand.Sum(nil)
}

func parsePublicKey(value string) (*ecdh.PublicKey, error) {
	raw, err := decode(value)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	return ecdh.X25519().NewPublicKey(raw)
}

func encode(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decode(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}
