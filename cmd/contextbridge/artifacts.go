package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/bridge"
)

func saveOutputArtifacts(output *bridge.Output, directory string) ([]string, int, error) {
	if output == nil || len(output.Artifacts) == 0 || strings.TrimSpace(directory) == "" {
		return nil, 0, nil
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, 0, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, 0, err
	}
	paths := make([]string, 0, len(output.Artifacts))
	references := 0
	for index := range output.Artifacts {
		artifact := &output.Artifacts[index]
		if artifact.DataBase64 == "" {
			if artifact.URL != "" {
				references++
			}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(artifact.DataBase64)
		if err != nil || len(data) == 0 {
			return paths, references, fmt.Errorf("artifact %q has invalid base64 data", artifact.Name)
		}
		if artifact.Size <= 0 || artifact.Size != len(data) {
			return paths, references, fmt.Errorf("artifact %q size does not match its data", artifact.Name)
		}
		expected, decodeErr := hex.DecodeString(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(artifact.SHA256)), "sha256:"))
		digest := sha256.Sum256(data)
		if decodeErr != nil || len(expected) != sha256.Size || !equalBytes(expected, digest[:]) {
			return paths, references, fmt.Errorf("artifact %q failed SHA256 verification", artifact.Name)
		}
		path, file, err := createArtifactFile(directory, artifact.Name)
		if err != nil {
			return paths, references, err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return paths, references, writeErr
		}
		if closeErr != nil {
			return paths, references, closeErr
		}
		artifact.DataBase64 = ""
		paths = append(paths, path)
	}
	return paths, references, nil
}

func materializeClusterArtifacts(raw []byte, directory string) ([]byte, []string, int, error) {
	if strings.TrimSpace(directory) == "" {
		return raw, nil, 0, nil
	}
	var submission bridge.Submission
	if err := json.Unmarshal(raw, &submission); err != nil {
		return raw, nil, 0, fmt.Errorf("decode cluster result for artifacts: %w", err)
	}
	paths, references, err := saveOutputArtifacts(submission.Output, directory)
	if err != nil {
		return raw, paths, references, err
	}
	updated, err := json.Marshal(submission)
	return updated, paths, references, err
}

func createArtifactFile(directory, name string) (string, *os.File, error) {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "" || name == "." {
		name = "artifact.bin"
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '-'
		}
		return r
	}, name)
	if len(name) > 180 {
		name = name[:180]
	}
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for index := 0; index < 1000; index++ {
		candidate := name
		if index > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, index+1, extension)
		}
		path := filepath.Join(directory, candidate)
		if rel, err := filepath.Rel(directory, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", nil, errors.New("artifact path escapes the output directory")
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return path, file, err
	}
	return "", nil, errors.New("could not choose a unique artifact filename")
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func reportSavedArtifacts(paths []string, references int) {
	for _, path := range paths {
		fmt.Fprintln(os.Stderr, "Saved artifact:", path)
	}
	if references > 0 {
		fmt.Fprintf(os.Stderr, "%d provider-hosted artifact reference(s) remain in the JSON result.\n", references)
	}
}
