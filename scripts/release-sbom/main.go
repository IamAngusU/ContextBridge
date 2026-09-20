package main

import (
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type bom struct {
	BOMFormat    string          `json:"bomFormat"`
	SpecVersion  string          `json:"specVersion"`
	Version      int             `json:"version"`
	Metadata     bomMetadata     `json:"metadata"`
	Components   []bomComponent  `json:"components,omitempty"`
	Dependencies []bomDependency `json:"dependencies,omitempty"`
}

type bomMetadata struct {
	Timestamp string       `json:"timestamp"`
	Component bomComponent `json:"component"`
}

type bomComponent struct {
	Type    string    `json:"type"`
	BOMRef  string    `json:"bom-ref"`
	Name    string    `json:"name"`
	Version string    `json:"version,omitempty"`
	PURL    string    `json:"purl,omitempty"`
	Hashes  []bomHash `json:"hashes,omitempty"`
}

type bomHash struct {
	Algorithm string `json:"alg"`
	Content   string `json:"content"`
}

type bomDependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: release-sbom BINARY VERSION SOURCE_DATE_EPOCH OUTPUT.json")
		os.Exit(2)
	}
	if err := writeSBOM(os.Args[1], os.Args[2], os.Args[3], os.Args[4]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeSBOM(binary, version, rawEpoch, output string) error {
	seconds, err := strconv.ParseInt(strings.TrimSpace(rawEpoch), 10, 64)
	if err != nil || seconds < 0 {
		return errors.New("SOURCE_DATE_EPOCH must be a non-negative Unix timestamp")
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("read Go build information: %w", err)
	}
	mainPath := strings.TrimSpace(info.Main.Path)
	if mainPath == "" {
		mainPath = strings.TrimSpace(info.Path)
	}
	if mainPath == "" {
		mainPath = "github.com/IamAngusU/ContextBridge/cmd/contextbridge"
	}
	mainRef := modulePURL(mainPath, version)
	document := bom{
		BOMFormat: "CycloneDX", SpecVersion: "1.5", Version: 1,
		Metadata: bomMetadata{
			Timestamp: time.Unix(seconds, 0).UTC().Format(time.RFC3339),
			Component: bomComponent{Type: "application", BOMRef: mainRef, Name: "ContextBridge", Version: strings.TrimPrefix(version, "v"), PURL: mainRef},
		},
	}
	dependencies := make([]string, 0, len(info.Deps))
	for _, dependency := range info.Deps {
		if dependency == nil {
			continue
		}
		module := dependency
		if dependency.Replace != nil {
			module = dependency.Replace
		}
		component := moduleComponent(module.Path, module.Version, module.Sum)
		document.Components = append(document.Components, component)
		dependencies = append(dependencies, component.BOMRef)
	}
	sort.Slice(document.Components, func(i, j int) bool { return document.Components[i].BOMRef < document.Components[j].BOMRef })
	sort.Strings(dependencies)
	document.Dependencies = []bomDependency{{Ref: mainRef, DependsOn: dependencies}}

	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(document)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(output)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(output)
		return closeErr
	}
	return nil
}

func moduleComponent(path, version, sum string) bomComponent {
	ref := modulePURL(path, version)
	component := bomComponent{Type: "library", BOMRef: ref, Name: path, Version: version, PURL: ref}
	if digest, ok := goModuleDigest(sum); ok {
		component.Hashes = []bomHash{{Algorithm: "SHA-256", Content: digest}}
	}
	return component
}

func modulePURL(path, version string) string {
	segments := strings.Split(strings.TrimSpace(path), "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	value := "pkg:golang/" + strings.Join(segments, "/")
	if strings.TrimSpace(version) != "" {
		value += "@" + url.PathEscape(strings.TrimSpace(version))
	}
	return value
}

func goModuleDigest(sum string) (string, bool) {
	if !strings.HasPrefix(sum, "h1:") {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sum, "h1:"))
	if err != nil || len(raw) != 32 {
		return "", false
	}
	return hex.EncodeToString(raw), true
}
