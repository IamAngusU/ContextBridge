package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const PolicyDecisionV1 = "contextbridge.policy-decision.v1"

const (
	PolicyCodeAllowed          = "policy.allowed"
	PolicyCodeDisabled         = "policy.disabled"
	PolicyCodeCostExceeded     = "policy.cost_budget_exceeded"
	PolicyCodeCostRequired     = "policy.cost_budget_required"
	PolicyCodeCostUnverifiable = "policy.cost_unverifiable"
	PolicyCodeEgressDenied     = "policy.egress_denied"
	PolicyCodeGroupDenied      = "policy.group_denied"
	PolicyCodeProviderDenied   = "policy.provider_denied"
	PolicyCodeTenantDenied     = "policy.tenant_denied"
	PolicyCodeTenantRequired   = "policy.tenant_required"
)

const maximumPolicyCostUSD = 1_000_000.0

// ExecutionPolicyConfig is the relay-authoritative execution boundary. It is
// deliberately smaller than a general policy language: bounded exact-match
// rules are inspectable, deterministic, and safe to include in receipts.
type ExecutionPolicyConfig struct {
	Enabled              bool                           `json:"enabled" yaml:"enabled"`
	TenantMode           string                         `json:"tenant_mode,omitempty" yaml:"tenant_mode,omitempty"`
	RequireTenant        bool                           `json:"require_tenant,omitempty" yaml:"require_tenant,omitempty"`
	LocalProviders       []string                       `json:"local_providers,omitempty" yaml:"local_providers,omitempty"`
	RemoteProviders      []string                       `json:"remote_providers,omitempty" yaml:"remote_providers,omitempty"`
	CostBoundedProviders []string                       `json:"cost_bounded_providers,omitempty" yaml:"cost_bounded_providers,omitempty"`
	Default              ExecutionPolicyRule            `json:"default,omitempty" yaml:"default,omitempty"`
	Tenants              map[string]ExecutionPolicyRule `json:"tenants,omitempty" yaml:"tenants,omitempty"`
}

// ExecutionPolicyRule is merged conservatively with the default rule. Tenant
// rules can add restrictions, but they cannot relax a default allow-list,
// egress boundary, cost requirement, or cost ceiling.
type ExecutionPolicyRule struct {
	Denied            bool     `json:"denied,omitempty" yaml:"denied,omitempty"`
	Egress            string   `json:"egress,omitempty" yaml:"egress,omitempty"`
	AllowedProviders  []string `json:"allowed_providers,omitempty" yaml:"allowed_providers,omitempty"`
	AllowedGroups     []string `json:"allowed_groups,omitempty" yaml:"allowed_groups,omitempty"`
	RequireCostBudget bool     `json:"require_cost_budget,omitempty" yaml:"require_cost_budget,omitempty"`
	MaxCostUSD        float64  `json:"max_cost_usd,omitempty" yaml:"max_cost_usd,omitempty"`
}

// PolicyDecision is durable, content-minimizing evidence. It intentionally
// excludes raw tenant IDs, prompts, tokens, and complete configuration.
type PolicyDecision struct {
	Schema                 string    `json:"schema"`
	Outcome                string    `json:"outcome"`
	ReasonCodes            []string  `json:"reason_codes"`
	RuleID                 string    `json:"rule_id"`
	PolicyFingerprint      string    `json:"policy_fingerprint_sha256"`
	EgressClass            string    `json:"egress_class"`
	CostBudgetUSD          float64   `json:"cost_budget_usd,omitempty"`
	CostEnforcement        string    `json:"cost_enforcement"`
	EvaluatedAt            time.Time `json:"evaluated_at"`
	ProviderClassification string    `json:"provider_classification"`
}

type PolicyViolation struct {
	code string
	err  error
}

func (e *PolicyViolation) Error() string { return e.err.Error() }
func (e *PolicyViolation) Unwrap() error { return e.err }
func (e *PolicyViolation) Code() string  { return e.code }

func rejectPolicy(code, message string) error {
	return &PolicyViolation{code: code, err: errors.New(message)}
}

func (cfg ExecutionPolicyConfig) Validate() error {
	switch cfg.TenantMode {
	case "", "open", "listed_only":
	default:
		return errors.New("cluster.policies.execution.tenant_mode must be open or listed_only")
	}
	for label, values := range map[string][]string{
		"local_providers": cfg.LocalProviders, "remote_providers": cfg.RemoteProviders,
		"cost_bounded_providers": cfg.CostBoundedProviders,
	} {
		if err := validatePolicyLabels(label, values); err != nil {
			return err
		}
	}
	for _, provider := range cfg.LocalProviders {
		if containsFold(cfg.RemoteProviders, provider) {
			return fmt.Errorf("execution policy provider %q cannot be both local and remote", provider)
		}
	}
	for _, provider := range cfg.CostBoundedProviders {
		if !containsFold(cfg.RemoteProviders, provider) {
			return fmt.Errorf("execution policy cost-bounded provider %q must also be classified remote", provider)
		}
		if strings.EqualFold(provider, "adapter") {
			return errors.New("execution policy cannot mark adapter as cost-bounded because adapter pricing is not enforceable before execution")
		}
	}
	if err := validateExecutionPolicyRule("default", cfg.Default); err != nil {
		return err
	}
	tenantKeys := map[string]string{}
	for tenant, rule := range cfg.Tenants {
		if !validRoutingLabel(tenant, 200) {
			return errors.New("cluster.policies.execution tenant keys must be bounded routing labels")
		}
		folded := strings.ToLower(tenant)
		if existing, found := tenantKeys[folded]; found {
			return fmt.Errorf("cluster.policies.execution tenant keys %q and %q are case-insensitively ambiguous", existing, tenant)
		}
		tenantKeys[folded] = tenant
		if err := validateExecutionPolicyRule("tenant rule", rule); err != nil {
			return fmt.Errorf("tenant %q: %w", tenant, err)
		}
	}
	return nil
}

func validateExecutionPolicyRule(name string, rule ExecutionPolicyRule) error {
	switch rule.Egress {
	case "", "any", "local_only", "remote_only":
	default:
		return fmt.Errorf("%s egress must be any, local_only, or remote_only", name)
	}
	if err := validatePolicyLabels(name+" allowed_providers", rule.AllowedProviders); err != nil {
		return err
	}
	if err := validatePolicyLabels(name+" allowed_groups", rule.AllowedGroups); err != nil {
		return err
	}
	if math.IsNaN(rule.MaxCostUSD) || math.IsInf(rule.MaxCostUSD, 0) || rule.MaxCostUSD < 0 || rule.MaxCostUSD > maximumPolicyCostUSD {
		return fmt.Errorf("%s max_cost_usd must be a finite value from 0 through %.0f", name, maximumPolicyCostUSD)
	}
	return nil
}

func validatePolicyLabels(name string, values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !validRoutingLabel(value, 160) {
			return fmt.Errorf("execution policy %s entries must be bounded routing labels", name)
		}
		key := strings.ToLower(value)
		if seen[key] {
			return fmt.Errorf("execution policy %s contains duplicate %q", name, value)
		}
		seen[key] = true
	}
	return nil
}

func validateExecutionPolicyRequirements(requirements Requirements) error {
	switch requirements.Egress {
	case "", "local_only", "remote_allowed":
	default:
		return errors.New("requirements.egress must be local_only or remote_allowed")
	}
	if math.IsNaN(requirements.MaxCostUSD) || math.IsInf(requirements.MaxCostUSD, 0) || requirements.MaxCostUSD < 0 || requirements.MaxCostUSD > maximumPolicyCostUSD {
		return fmt.Errorf("requirements.max_cost_usd must be a finite value from 0 through %.0f", maximumPolicyCostUSD)
	}
	return nil
}

func EvaluateExecutionPolicy(cfg ExecutionPolicyConfig, tenant string, requirements Requirements, now time.Time) (PolicyDecision, error) {
	normalized := normalizeExecutionPolicyConfig(cfg)
	decision := PolicyDecision{
		Schema: PolicyDecisionV1, Outcome: "allow", RuleID: "default",
		PolicyFingerprint: executionPolicyFingerprint(normalized), EvaluatedAt: now.UTC(),
		ProviderClassification: classifyPolicyProvider(normalized, requirements.Provider),
		CostBudgetUSD:          requirements.MaxCostUSD,
	}
	decision.EgressClass = decision.ProviderClassification
	if !normalized.Enabled {
		decision.ReasonCodes = []string{PolicyCodeDisabled}
		decision.CostEnforcement = "not_configured"
		return decision, nil
	}

	tenant = strings.TrimSpace(tenant)
	if normalized.RequireTenant && tenant == "" {
		return deniedPolicy(decision, PolicyCodeTenantRequired, "execution policy requires tenant_id")
	}
	tenantRule, listed := normalized.Tenants[tenant]
	if normalized.TenantMode == "listed_only" && (tenant == "" || !listed) {
		return deniedPolicy(decision, PolicyCodeTenantDenied, "execution policy does not allow this tenant")
	}
	rule := normalized.Default
	if listed {
		decision.RuleID = policyTenantRuleID(tenant)
		rule = mergeExecutionPolicyRules(rule, tenantRule)
	}
	if rule.Denied {
		return deniedPolicy(decision, PolicyCodeTenantDenied, "execution policy denies this tenant")
	}
	if len(rule.AllowedGroups) > 0 && !containsFold(rule.AllowedGroups, requirements.Group) {
		return deniedPolicy(decision, PolicyCodeGroupDenied, "execution policy denies the requested group")
	}
	if len(rule.AllowedProviders) > 0 && !containsFold(rule.AllowedProviders, requirements.Provider) {
		return deniedPolicy(decision, PolicyCodeProviderDenied, "execution policy denies the requested provider")
	}
	if decision.EgressClass == "unknown" {
		return deniedPolicy(decision, PolicyCodeEgressDenied, "execution policy cannot classify the requested provider as local or remote")
	}
	if requirements.Egress == "local_only" && decision.EgressClass != "local" {
		return deniedPolicy(decision, PolicyCodeEgressDenied, "job requires local-only execution but the provider is not classified local")
	}
	switch rule.Egress {
	case "local_only":
		if decision.EgressClass != "local" {
			return deniedPolicy(decision, PolicyCodeEgressDenied, "execution policy permits only local providers")
		}
	case "remote_only":
		if decision.EgressClass != "remote" {
			return deniedPolicy(decision, PolicyCodeEgressDenied, "execution policy permits only remote providers")
		}
	}

	budget := requirements.MaxCostUSD
	if decision.EgressClass == "remote" && (rule.RequireCostBudget || rule.MaxCostUSD > 0) && budget == 0 {
		return deniedPolicy(decision, PolicyCodeCostRequired, "execution policy requires max_cost_usd")
	}
	if decision.EgressClass == "remote" && rule.MaxCostUSD > 0 && budget > rule.MaxCostUSD {
		return deniedPolicy(decision, PolicyCodeCostExceeded, "requested cost budget exceeds the execution policy ceiling")
	}
	switch {
	case budget == 0 && decision.EgressClass == "remote":
		decision.CostEnforcement = "unbounded_unknown"
	case budget == 0:
		decision.CostEnforcement = "not_requested"
	case decision.EgressClass == "local":
		decision.CostEnforcement = "not_applicable_local"
	case decision.EgressClass == "remote" && containsFold(normalized.CostBoundedProviders, requirements.Provider):
		decision.CostEnforcement = "worker_upper_bound"
	default:
		return deniedPolicy(decision, PolicyCodeCostUnverifiable, "the requested provider has no operator-reviewed cost upper-bound enforcement")
	}
	decision.ReasonCodes = []string{PolicyCodeAllowed}
	return decision, nil
}

func deniedPolicy(decision PolicyDecision, code, message string) (PolicyDecision, error) {
	decision.Outcome = "deny"
	decision.ReasonCodes = []string{code}
	if decision.CostEnforcement == "" {
		decision.CostEnforcement = "not_evaluated"
	}
	return decision, rejectPolicy(code, message)
}

func (decision PolicyDecision) ValidateAllowed() error {
	if decision.Schema == "" {
		return nil // retained pre-policy jobs and reservations
	}
	if decision.Schema != PolicyDecisionV1 || decision.Outcome != "allow" {
		return errors.New("policy decision must be an allowed Policy Decision v1")
	}
	if len(decision.ReasonCodes) != 1 || (decision.ReasonCodes[0] != PolicyCodeAllowed && decision.ReasonCodes[0] != PolicyCodeDisabled) {
		return errors.New("allowed policy decision has an invalid reason code")
	}
	fingerprint := strings.TrimPrefix(decision.PolicyFingerprint, "sha256:")
	decodedFingerprint, fingerprintErr := hex.DecodeString(fingerprint)
	if !validPolicyRuleID(decision.RuleID) || !strings.HasPrefix(decision.PolicyFingerprint, "sha256:") || fingerprintErr != nil || len(decodedFingerprint) != sha256.Size {
		return errors.New("policy decision identity is incomplete")
	}
	if decision.ProviderClassification != "local" && decision.ProviderClassification != "remote" && decision.ProviderClassification != "unknown" {
		return errors.New("policy decision provider classification is invalid")
	}
	if decision.EgressClass != decision.ProviderClassification || decision.EvaluatedAt.IsZero() {
		return errors.New("policy decision evidence is incomplete")
	}
	switch decision.CostEnforcement {
	case "not_configured":
		if decision.ReasonCodes[0] != PolicyCodeDisabled {
			return errors.New("policy decision has contradictory disabled cost evidence")
		}
	case "not_requested", "not_applicable_local", "unbounded_unknown", "worker_upper_bound":
		if decision.ReasonCodes[0] == PolicyCodeDisabled || decision.ProviderClassification == "unknown" {
			return errors.New("policy decision has contradictory enabled cost evidence")
		}
	default:
		return errors.New("policy decision cost enforcement is invalid")
	}
	if decision.CostEnforcement == "worker_upper_bound" && (decision.ProviderClassification != "remote" || decision.CostBudgetUSD <= 0) {
		return errors.New("policy decision cost upper-bound evidence is incomplete")
	}
	if decision.CostEnforcement == "not_requested" && (decision.ProviderClassification != "local" || decision.CostBudgetUSD != 0) {
		return errors.New("policy decision no-budget evidence is contradictory")
	}
	if decision.CostEnforcement == "not_applicable_local" && (decision.ProviderClassification != "local" || decision.CostBudgetUSD <= 0) {
		return errors.New("policy decision local cost evidence is contradictory")
	}
	if decision.CostEnforcement == "unbounded_unknown" && (decision.ProviderClassification != "remote" || decision.CostBudgetUSD != 0) {
		return errors.New("policy decision unbounded cost evidence is contradictory")
	}
	if math.IsNaN(decision.CostBudgetUSD) || math.IsInf(decision.CostBudgetUSD, 0) || decision.CostBudgetUSD < 0 || decision.CostBudgetUSD > maximumPolicyCostUSD {
		return errors.New("policy decision cost budget is invalid")
	}
	return nil
}

func validPolicyRuleID(ruleID string) bool {
	if ruleID == "default" {
		return true
	}
	digest := strings.TrimPrefix(ruleID, "tenant:sha256:")
	decoded, err := hex.DecodeString(digest)
	return strings.HasPrefix(ruleID, "tenant:sha256:") && err == nil && len(decoded) == sha256.Size
}

func mergeExecutionPolicyRules(base, tenant ExecutionPolicyRule) ExecutionPolicyRule {
	result := base
	result.Denied = base.Denied || tenant.Denied
	if tenant.Egress != "" && tenant.Egress != "any" {
		if result.Egress == "" || result.Egress == "any" || result.Egress == tenant.Egress {
			result.Egress = tenant.Egress
		} else {
			// Conflicting local-only and remote-only boundaries have an empty
			// intersection. Deny the tenant rather than choosing either side.
			result.Denied = true
		}
	}
	result.AllowedProviders = intersectPolicyLists(base.AllowedProviders, tenant.AllowedProviders)
	if len(base.AllowedProviders) > 0 && len(tenant.AllowedProviders) > 0 && len(result.AllowedProviders) == 0 {
		result.Denied = true
	}
	result.AllowedGroups = intersectPolicyLists(base.AllowedGroups, tenant.AllowedGroups)
	if len(base.AllowedGroups) > 0 && len(tenant.AllowedGroups) > 0 && len(result.AllowedGroups) == 0 {
		result.Denied = true
	}
	result.RequireCostBudget = base.RequireCostBudget || tenant.RequireCostBudget
	if result.MaxCostUSD == 0 || tenant.MaxCostUSD > 0 && tenant.MaxCostUSD < result.MaxCostUSD {
		result.MaxCostUSD = tenant.MaxCostUSD
	}
	return result
}

func intersectPolicyLists(base, tenant []string) []string {
	if len(base) == 0 {
		return append([]string(nil), tenant...)
	}
	if len(tenant) == 0 {
		return append([]string(nil), base...)
	}
	result := make([]string, 0, len(base))
	for _, value := range base {
		if containsFold(tenant, value) {
			result = append(result, value)
		}
	}
	return result
}

func classifyPolicyProvider(cfg ExecutionPolicyConfig, provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "unknown"
	}
	if containsFold(cfg.LocalProviders, provider) {
		return "local"
	}
	if containsFold(cfg.RemoteProviders, provider) {
		return "remote"
	}
	return "unknown"
}

func normalizeExecutionPolicyConfig(cfg ExecutionPolicyConfig) ExecutionPolicyConfig {
	if cfg.TenantMode == "" {
		cfg.TenantMode = "open"
	}
	cfg.LocalProviders = normalizedPolicyList(cfg.LocalProviders)
	cfg.RemoteProviders = normalizedPolicyList(cfg.RemoteProviders)
	cfg.CostBoundedProviders = normalizedPolicyList(cfg.CostBoundedProviders)
	cfg.Default = normalizeExecutionPolicyRule(cfg.Default)
	normalizedTenants := make(map[string]ExecutionPolicyRule, len(cfg.Tenants))
	for tenant, rule := range cfg.Tenants {
		normalizedTenants[tenant] = normalizeExecutionPolicyRule(rule)
	}
	cfg.Tenants = normalizedTenants
	return cfg
}

func normalizeExecutionPolicyRule(rule ExecutionPolicyRule) ExecutionPolicyRule {
	if rule.Egress == "" {
		rule.Egress = "any"
	}
	rule.AllowedProviders = normalizedPolicyList(rule.AllowedProviders)
	rule.AllowedGroups = normalizedPolicyList(rule.AllowedGroups)
	return rule
}

func normalizedPolicyList(values []string) []string {
	result := append([]string(nil), values...)
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func executionPolicyFingerprint(cfg ExecutionPolicyConfig) string {
	encoded, _ := json.Marshal(cfg)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func policyTenantRuleID(tenant string) string {
	digest := sha256.Sum256([]byte(tenant))
	return fmt.Sprintf("tenant:sha256:%x", digest[:])
}

// EquivalentAuthorization compares the durable authorization facts while
// deliberately ignoring evaluation time. It is used when a one-time E2EE
// reservation is promoted: a policy/config change must invalidate the old
// approval, while a second evaluation of the same policy remains equivalent.
func (decision PolicyDecision) EquivalentAuthorization(other PolicyDecision) bool {
	left := decision
	right := other
	left.EvaluatedAt = time.Time{}
	right.EvaluatedAt = time.Time{}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
