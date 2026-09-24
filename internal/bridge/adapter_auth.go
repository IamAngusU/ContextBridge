package bridge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

const adapterProtocolV2 = "contextbridge.adapter.v2"

type adapterPrincipalIdentity struct {
	id       string
	profiles map[string]struct{}
	token    [sha256.Size]byte
}

type adapterPrincipalContextKey struct{}

func configuredAdapterPrincipals(cfg config.Config) []adapterPrincipalIdentity {
	principals := make([]adapterPrincipalIdentity, 0, len(cfg.Providers.Adapter.Principals))
	for id, configured := range cfg.Providers.Adapter.Principals {
		profiles := make(map[string]struct{}, len(configured.AllowedProfiles))
		for _, profile := range configured.AllowedProfiles {
			profiles[strings.TrimSpace(profile)] = struct{}{}
		}
		principals = append(principals, adapterPrincipalIdentity{
			id: id, profiles: profiles, token: sha256.Sum256([]byte(strings.TrimSpace(configured.EffectiveToken()))),
		})
	}
	return principals
}

func (s *Server) adapterAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := bearerToken(r)
		providedDigest := sha256.Sum256([]byte(provided))
		var matched *adapterPrincipalIdentity
		for index := range s.adapterPrincipals {
			candidate := &s.adapterPrincipals[index]
			if subtle.ConstantTimeCompare(providedDigest[:], candidate.token[:]) == 1 && provided != "" {
				matched = candidate
			}
		}
		if matched == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid scoped adapter credential required"})
			return
		}
		identity := *matched
		next(w, r.WithContext(context.WithValue(r.Context(), adapterPrincipalContextKey{}, identity)))
	}
}

func (s *Server) legacyAdapterAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adapterAuthMode() != "dual" {
			w.Header().Set("Sunset", "true")
			writeJSON(w, http.StatusGone, map[string]string{
				"error": "legacy adapter protocol is disabled; use /v2/adapter with a scoped credential",
			})
			return
		}
		s.auth(next)(w, r)
	}
}

func (s *Server) adapterAuthMode() string {
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Providers.Adapter.AuthMode))
	if mode == "" {
		return "scoped"
	}
	return mode
}

func adapterPrincipalFromRequest(r *http.Request) (adapterPrincipalIdentity, bool) {
	identity, ok := r.Context().Value(adapterPrincipalContextKey{}).(adapterPrincipalIdentity)
	return identity, ok
}

func (identity adapterPrincipalIdentity) allowsProfile(profile string) bool {
	_, ok := identity.profiles[strings.TrimSpace(profile)]
	return ok
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < len("Bearer ") || !strings.EqualFold(value[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[len("Bearer "):])
}

func (s *Server) handleAdapterStatusV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET required"})
		return
	}
	identity, ok := adapterPrincipalFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "adapter identity missing"})
		return
	}
	profiles := make([]string, 0, len(identity.profiles))
	for profile := range identity.profiles {
		profiles = append(profiles, profile)
	}
	slicesSort(profiles)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "protocol": adapterProtocolV2, "principal_id": identity.id, "allowed_profiles": profiles,
	})
}

func slicesSort(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}
