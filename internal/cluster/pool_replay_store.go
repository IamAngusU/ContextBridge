package cluster

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	bolt "go.etcd.io/bbolt"
)

var poolAuthorizationClaimsBucket = []byte("pool_authorization_claims_v1")

const poolReplayStoreLockTimeout = 5 * time.Second

// claimPoolJobAuthorization durably consumes one customer-signed job
// authorization before dispatch. The sidecar database is tied to the worker
// identity so a normal process or machine restart cannot reopen an already
// accepted authorization. Any persistence failure is fail-closed.
func (w *Worker) claimPoolJobAuthorization(job Job, now time.Time) error {
	if w.identity.PoolCertificate == nil {
		return nil
	}
	if job.PoolAuthorization == nil || job.PoolAuthorization.Signature == "" {
		return errors.New("customer pool authorization is required")
	}
	if w.cfg.IdentityFile == "" {
		return errors.New("customer pool replay protection requires a durable worker identity file")
	}

	w.poolReplayMu.Lock()
	defer w.poolReplayMu.Unlock()
	return claimPoolAuthorization(
		poolReplayStorePath(w.cfg.IdentityFile),
		job.PoolAuthorization.Signature,
		job.PoolAuthorization.ExpiresAt.UTC(),
		now.UTC(),
	)
}

func poolReplayStorePath(identityFile string) string {
	return filepath.Clean(identityFile) + ".pool-replay.db"
}

func claimPoolAuthorization(path, signature string, expiresAt, now time.Time) error {
	if signature == "" || !expiresAt.After(now) {
		return errors.New("customer pool authorization is missing or expired")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("customer pool replay store must be a regular non-symlink file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return errors.New("customer pool replay store permissions must deny group and other access")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect customer pool replay store: %w", err)
	}

	database, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: poolReplayStoreLockTimeout})
	if err != nil {
		return fmt.Errorf("open customer pool replay store: %w", err)
	}
	claimErr := database.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(poolAuthorizationClaimsBucket)
		if err != nil {
			return err
		}
		claimKey := sha256.Sum256([]byte(signature))
		if encoded := bucket.Get(claimKey[:]); len(encoded) == 8 {
			claimedUntil := time.Unix(0, int64(binary.BigEndian.Uint64(encoded))).UTC()
			if claimedUntil.After(now) {
				return errors.New("customer pool authorization was already claimed")
			}
			if err := bucket.Delete(claimKey[:]); err != nil {
				return err
			}
		}

		if bucket.Stats().KeyN >= maximumPoolAuthorizationReplayEntries {
			cursor := bucket.Cursor()
			for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
				if len(value) != 8 || !time.Unix(0, int64(binary.BigEndian.Uint64(value))).After(now) {
					if err := cursor.Delete(); err != nil {
						return err
					}
				}
			}
		}
		if bucket.Stats().KeyN >= maximumPoolAuthorizationReplayEntries {
			return errors.New("customer pool replay store is full")
		}
		var encodedExpiry [8]byte
		binary.BigEndian.PutUint64(encodedExpiry[:], uint64(expiresAt.UnixNano()))
		return bucket.Put(claimKey[:], encodedExpiry[:])
	})
	closeErr := database.Close()
	if claimErr != nil {
		return claimErr
	}
	if closeErr != nil {
		return fmt.Errorf("close customer pool replay store: %w", closeErr)
	}
	return nil
}
