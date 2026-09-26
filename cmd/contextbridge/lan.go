package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
	"github.com/IamAngusU/ContextBridge/internal/config"
)

var pinnedClusterClients sync.Map // map[scheme://host]*http.Client

func clusterLANCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: contextbridge cluster lan init|relocate|join|status [options]")
	}
	switch args[0] {
	case "init":
		return clusterLANInitCommand(args[1:])
	case "relocate":
		return clusterLANRelocateCommand(args[1:])
	case "join":
		return clusterLANJoinCommand(args[1:])
	case "status":
		return clusterLANStatusCommand(args[1:])
	default:
		return fmt.Errorf("unknown cluster LAN command %s", args[0])
	}
}

func clusterLANRelocateCommand(args []string) error {
	flags := flag.NewFlagSet("cluster lan relocate", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	listen := flags.String("listen", "", "new private/wildcard LAN listen address")
	advertiseHost := flags.String("advertise-host", "", "new LAN IP or local DNS name")
	certificateOut := flags.String("certificate-out", "", "new certificate path; existing relay private key is retained")
	out := flags.String("out", "", "new public relocation/join-bundle path")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*advertiseHost) == "" {
		return errors.New("usage: contextbridge cluster lan relocate --advertise-host HOST [--listen HOST:PORT] [--out FILE]")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if !cfg.Cluster.Relay.LAN.Enabled || strings.TrimSpace(cfg.Cluster.Relay.LAN.PublicURL) == "" {
		return errors.New("LAN relay is not configured; run `contextbridge cluster lan init` first")
	}
	currentURL, err := url.Parse(cfg.Cluster.Relay.LAN.PublicURL)
	if err != nil || currentURL.Hostname() == "" || currentURL.Port() == "" {
		return errors.New("configured LAN public URL is invalid")
	}
	currentTrust, err := cluster.LoadLANTLSIdentity(cfg.Cluster.Relay.LAN.CertificateFile, cfg.Cluster.Relay.LAN.PrivateKeyFile, currentURL.Hostname(), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("load existing LAN identity: %w", err)
	}
	*advertiseHost = strings.Trim(strings.TrimSpace(*advertiseHost), "[]")
	if parsed := net.ParseIP(*advertiseHost); parsed != nil && !parsed.IsPrivate() && !parsed.IsLinkLocalUnicast() && !parsed.IsLoopback() {
		return errors.New("--advertise-host IP must be private, link-local, or loopback")
	}
	if *listen == "" {
		*listen = net.JoinHostPort(*advertiseHost, currentURL.Port())
	}
	wildcardListen, err := validateLANListenAdvertisement(*listen, *advertiseHost)
	if err != nil {
		return err
	}
	newURL := "https://" + net.JoinHostPort(*advertiseHost, mustLANPort(*listen))
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(newURL))))[:12]
	if *certificateOut == "" {
		*certificateOut = filepath.Join(filepath.Dir(cfg.Cluster.Relay.LAN.CertificateFile), "relay-cert-"+digest+".pem")
	}
	newTrust, err := cluster.IssueLANTLSCertificateForExistingKey(*certificateOut, cfg.Cluster.Relay.LAN.PrivateKeyFile, *advertiseHost, time.Now().UTC())
	if err != nil {
		return err
	}
	if !strings.EqualFold(currentTrust.SPKISHA256, newTrust.SPKISHA256) {
		return errors.New("new LAN certificate did not retain the existing relay identity")
	}
	if *out == "" {
		*out = filepath.Join(filepath.Dir(*path), "contextbridge-lan-relocate-"+digest+".json")
	}
	bundle := cluster.LANJoinBundle{Version: cluster.LANJoinBundleVersion, RelayURL: newURL, Trust: newTrust, CreatedAt: time.Now().UTC()}
	if err := cluster.SaveLANJoinBundle(*out, bundle); err != nil {
		return fmt.Errorf("save LAN relocation bundle: %w", err)
	}
	cfg.Cluster.Relay.LAN.Listen = *listen
	cfg.Cluster.Relay.LAN.PublicURL = newURL
	cfg.Cluster.Relay.LAN.CertificateFile = *certificateOut
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	fmt.Println("LAN relay address updated with the same pinned identity")
	fmt.Println("  Relay:", newURL)
	fmt.Println("  Identity:", newTrust.SPKISHA256)
	fmt.Println("  Relocation bundle:", *out)
	if wildcardListen {
		fmt.Println("  WARNING: explicit wildcard listener exposes the LAN TLS service on every matching network interface")
	}
	fmt.Println("Restart the relay, transfer this bundle through a trusted channel, then run on each existing worker:")
	fmt.Printf("  contextbridge cluster lan join --bundle %q\n", *out)
	fmt.Println("The worker updates only if the live endpoint proves the same already-pinned relay key.")
	return nil
}

func clusterLANInitCommand(args []string) error {
	flags := flag.NewFlagSet("cluster lan init", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	listen := flags.String("listen", "", "private/wildcard LAN listen address")
	advertiseHost := flags.String("advertise-host", "", "LAN IP or local DNS name workers can reach")
	out := flags.String("out", "", "new public join-bundle path")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("cluster lan init does not accept positional arguments")
	}
	explicitListen := commandFlagSpecified(args, "listen")
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *listen == "" {
		*listen = cfg.Cluster.Relay.LAN.Listen
	}
	_, port, err := net.SplitHostPort(strings.TrimSpace(*listen))
	if err != nil || port == "" {
		return errors.New("--listen must be a host:port address")
	}
	if strings.TrimSpace(*advertiseHost) == "" {
		*advertiseHost, err = preferredLANAddress()
		if err != nil {
			return err
		}
	}
	*advertiseHost = strings.Trim(strings.TrimSpace(*advertiseHost), "[]")
	if parsed := net.ParseIP(*advertiseHost); parsed != nil && !parsed.IsPrivate() && !parsed.IsLinkLocalUnicast() && !parsed.IsLoopback() {
		return errors.New("--advertise-host IP must be private, link-local, or loopback")
	}
	if !explicitListen {
		// The generated join bundle names exactly one reachable LAN endpoint, so
		// the safe default is to expose TLS on that interface only. Keep the
		// configured/default port, but never inherit a wildcard host implicitly.
		*listen = net.JoinHostPort(*advertiseHost, port)
	}
	wildcardListen, err := validateLANListenAdvertisement(*listen, *advertiseHost)
	if err != nil {
		return err
	}
	relayURL := "https://" + net.JoinHostPort(*advertiseHost, port)
	trust, err := cluster.EnsureLANTLSIdentity(cfg.Cluster.Relay.LAN.CertificateFile, cfg.Cluster.Relay.LAN.PrivateKeyFile, *advertiseHost, time.Now().UTC())
	if err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(filepath.Dir(*path), "contextbridge-lan-join.json")
	}
	bundle := cluster.LANJoinBundle{Version: cluster.LANJoinBundleVersion, RelayURL: relayURL, Trust: trust, CreatedAt: time.Now().UTC()}
	if err := cluster.SaveLANJoinBundle(*out, bundle); err != nil {
		return fmt.Errorf("save LAN join bundle: %w", err)
	}
	cfg.Cluster.Relay.Enabled = true
	cfg.Cluster.Relay.LAN.Enabled = true
	cfg.Cluster.Relay.LAN.Listen = *listen
	cfg.Cluster.Relay.LAN.PublicURL = relayURL
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	fmt.Println("Secure offline LAN relay configured")
	fmt.Println("  Relay:", relayURL)
	fmt.Println("  Identity:", trust.SPKISHA256)
	fmt.Println("  Join bundle:", *out)
	if wildcardListen {
		fmt.Println("  WARNING: explicit wildcard listener exposes the LAN TLS service on every matching network interface")
	}
	fmt.Println("Transfer the join bundle through a trusted local channel, restart ContextBridge, then run on the other device:")
	fmt.Printf("  contextbridge cluster lan join --bundle %q\n", *out)
	fmt.Println("Discovery is not trust. A changed bundle or TLS identity is rejected.")
	return nil
}

func commandFlagSpecified(args []string, name string) bool {
	wanted := "--" + name
	for _, argument := range args {
		if argument == "--" {
			return false
		}
		if argument == wanted || strings.HasPrefix(argument, wanted+"=") {
			return true
		}
	}
	return false
}

func validateLANListenAdvertisement(listen, advertiseHost string) (bool, error) {
	if err := cluster.ValidateLANListenerEndpoint(listen, "https://"+net.JoinHostPort(strings.Trim(strings.TrimSpace(advertiseHost), "[]"), mustLANPort(listen))); err != nil {
		return false, err
	}
	host, _, _ := net.SplitHostPort(strings.TrimSpace(listen))
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsUnspecified(), nil
}

func mustLANPort(listen string) string {
	_, port, _ := net.SplitHostPort(strings.TrimSpace(listen))
	return port
}

func clusterLANJoinCommand(args []string) error {
	flags := flag.NewFlagSet("cluster lan join", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	bundlePath := flags.String("bundle", "", "trusted LAN join-bundle path")
	name := flags.String("name", "auto", "worker node name")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*bundlePath) == "" {
		return errors.New("usage: contextbridge cluster lan join --bundle FILE [--name NAME] [--config PATH]")
	}
	bundle, err := cluster.LoadLANJoinBundle(*bundlePath)
	if err != nil {
		return fmt.Errorf("load LAN join bundle: %w", err)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *name == "" || *name == "auto" {
		*name, _ = os.Hostname()
	}
	if _, err := os.Stat(cfg.Cluster.Worker.IdentityFile); err == nil {
		existingURL, existing, trustErr := cluster.LoadWorkerRelayBinding(cfg.Cluster.Worker.IdentityFile)
		if trustErr != nil || existing.SPKISHA256 == "" || !strings.EqualFold(existing.SPKISHA256, bundle.Trust.SPKISHA256) {
			return errors.New("an existing worker identity belongs to another relay or TLS identity; preserve it and use a fresh identity path")
		}
		if strings.TrimRight(existingURL, "/") != strings.TrimRight(bundle.RelayURL, "/") || existing.CertificatePEM != bundle.Trust.CertificatePEM {
			if err := verifyPinnedRelayHealth(context.Background(), bundle); err != nil {
				return fmt.Errorf("new LAN endpoint did not prove the existing pinned identity: %w", err)
			}
			if err := cluster.RebindWorkerRelayTrust(cfg.Cluster.Worker.IdentityFile, bundle.RelayURL, bundle.Trust); err != nil {
				return err
			}
			previousURL := cfg.Cluster.Worker.RelayURL
			cfg.Cluster.Worker.Enabled = true
			cfg.Cluster.Worker.RelayURL = bundle.RelayURL
			if err := config.Save(*path, cfg); err != nil {
				rollbackErr := cluster.RebindWorkerRelayTrust(cfg.Cluster.Worker.IdentityFile, existingURL, existing)
				cfg.Cluster.Worker.RelayURL = previousURL
				if rollbackErr != nil {
					return fmt.Errorf("save relocated worker config: %w; identity rollback also failed: %v", err, rollbackErr)
				}
				return err
			}
			pinnedClusterClients.Delete(httpOrigin(existingURL))
			registerClusterTrust(cfg, bundle.RelayURL)
			fmt.Println("Existing LAN worker identity moved to the new address after same-key TLS proof. Restart ContextBridge to reconnect.")
			return nil
		}
		cfg.Cluster.Worker.Enabled = true
		cfg.Cluster.Worker.RelayURL = bundle.RelayURL
		if err := config.Save(*path, cfg); err != nil {
			return err
		}
		fmt.Println("Existing pinned LAN worker identity retained. Restart ContextBridge to join the pool.")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := cluster.PairWorkerWithTrust(ctx, bundle.RelayURL, *name, cfg.Cluster.Worker.IdentityFile, cfg.Cluster.Worker.Groups, bundle.Trust, func(pair cluster.PairResponse) {
		fmt.Println("Approve this LAN worker on the relay")
		fmt.Println("  Code:", pair.UserCode)
		fmt.Println("  Relay identity:", bundle.Trust.SPKISHA256)
		fmt.Println("Waiting for approval. The code expires at", pair.ExpiresAt.Local().Format(time.RFC1123))
	}); err != nil {
		return err
	}
	cfg.Cluster.Worker.Enabled = true
	cfg.Cluster.Worker.RelayURL = bundle.RelayURL
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	registerClusterTrust(cfg, bundle.RelayURL)
	fmt.Println("LAN worker paired with a pinned relay identity. Restart ContextBridge to join the pool.")
	return nil
}

func verifyPinnedRelayHealth(parent context.Context, bundle cluster.LANJoinBundle) error {
	client, err := cluster.NewRelayHTTPClient(bundle.RelayURL, bundle.Trust, 10*time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(bundle.RelayURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("relay health returned %s", response.Status)
	}
	return nil
}

func clusterLANStatusCommand(args []string) error {
	flags := flag.NewFlagSet("cluster lan status", flag.ContinueOnError)
	path := flags.String("config", defaultConfigPath(), "config path")
	if err := parseInterspersedFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("cluster lan status does not accept positional arguments")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if cfg.Cluster.Relay.LAN.Enabled {
		fmt.Println("LAN relay     enabled")
		fmt.Println("LAN endpoint  ", cfg.Cluster.Relay.LAN.PublicURL)
	} else {
		fmt.Println("LAN relay     disabled")
	}
	if cfg.Cluster.Worker.RelayURL == "" {
		fmt.Println("LAN worker    not configured")
		return nil
	}
	trust, trustErr := cluster.LoadWorkerRelayTrust(cfg.Cluster.Worker.IdentityFile, cfg.Cluster.Worker.RelayURL)
	if trustErr != nil {
		fmt.Println("LAN worker    unavailable:", trustErr)
		return nil
	}
	if trust.SPKISHA256 == "" {
		fmt.Println("LAN worker    standard PKI/loopback relay")
		return nil
	}
	fmt.Println("LAN worker    pinned")
	fmt.Println("Relay identity", trust.SPKISHA256)
	return nil
}

func preferredLANAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var ipv4, ipv6 []string
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, raw := range addresses {
			host, _, splitErr := net.ParseCIDR(raw.String())
			if splitErr != nil || (!host.IsPrivate() && !host.IsLinkLocalUnicast()) {
				continue
			}
			if host.To4() != nil {
				ipv4 = append(ipv4, host.String())
			} else {
				ipv6 = append(ipv6, host.String())
			}
		}
	}
	sort.Strings(ipv4)
	sort.Strings(ipv6)
	if len(ipv4) == 1 {
		return ipv4[0], nil
	}
	if len(ipv4) > 1 {
		return "", fmt.Errorf("multiple private LAN addresses detected (%s); pass the intended one with --advertise-host", strings.Join(ipv4, ", "))
	}
	if len(ipv6) == 1 {
		return ipv6[0], nil
	}
	if len(ipv6) > 1 {
		return "", fmt.Errorf("multiple private LAN addresses detected (%s); pass the intended one with --advertise-host", strings.Join(ipv6, ", "))
	}
	return "", errors.New("no private or link-local LAN address detected; pass --advertise-host explicitly")
}

func registerClusterTrust(cfg config.Config, target string) {
	targetOrigin := httpOrigin(target)
	relayOrigin := httpOrigin(cfg.Cluster.Worker.RelayURL)
	if targetOrigin == "" {
		return
	}
	if strings.TrimSpace(cfg.Cluster.Worker.IdentityFile) == "" || relayOrigin == "" || targetOrigin != relayOrigin {
		pinnedClusterClients.Delete(targetOrigin)
		return
	}
	trust, err := cluster.LoadWorkerRelayTrust(cfg.Cluster.Worker.IdentityFile, cfg.Cluster.Worker.RelayURL)
	if err != nil || trust.SPKISHA256 == "" {
		pinnedClusterClients.Delete(targetOrigin)
		return
	}
	client, err := cluster.NewRelayHTTPClient(target, trust, 0)
	if err != nil {
		pinnedClusterClients.Delete(targetOrigin)
		return
	}
	pinnedClusterClients.Store(targetOrigin, client)
}

func clusterHTTPClient(target string) *http.Client {
	if client, ok := pinnedClusterClients.Load(httpOrigin(target)); ok {
		return client.(*http.Client)
	}
	return http.DefaultClient
}

func httpOrigin(target string) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}
