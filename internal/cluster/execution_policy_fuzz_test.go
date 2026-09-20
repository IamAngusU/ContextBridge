package cluster

import (
	"math"
	"testing"
)

// FuzzTenantPolicyNeverBroadensDefault turns the central policy promise into
// an executable property: a tenant rule may narrow authority, but it can never
// regain a provider, group, egress class, optional budget, or cost ceiling that
// the default rule removed.
func FuzzTenantPolicyNeverBroadensDefault(f *testing.F) {
	for _, seed := range []struct {
		baseMask, tenantMask       uint8
		baseEgress, tenantEgress   uint8
		baseDenied, tenantDenied   bool
		baseRequire, tenantRequire bool
		baseCost, tenantCost       uint32
	}{
		{3, 7, 1, 0, false, false, true, false, 100, 500},
		{7, 1, 1, 2, false, false, false, false, 500, 100},
		{0, 7, 0, 2, false, false, false, true, 0, 100},
		{7, 7, 2, 1, true, false, true, false, 1, 0},
	} {
		f.Add(seed.baseMask, seed.tenantMask, seed.baseEgress, seed.tenantEgress,
			seed.baseDenied, seed.tenantDenied, seed.baseRequire, seed.tenantRequire, seed.baseCost, seed.tenantCost)
	}
	f.Fuzz(func(t *testing.T, baseMask, tenantMask, baseEgressCode, tenantEgressCode uint8,
		baseDenied, tenantDenied, baseRequire, tenantRequire bool, baseCostRaw, tenantCostRaw uint32) {
		labels := []string{"ollama", "adapter", "deepseek"}
		list := func(mask uint8) []string {
			values := make([]string, 0, len(labels))
			for index, label := range labels {
				if mask&(1<<index) != 0 {
					values = append(values, label)
				}
			}
			return values
		}
		egress := func(code uint8) string {
			return []string{"", "local_only", "remote_only", "any"}[code%4]
		}
		cost := func(raw uint32) float64 {
			if raw%5 == 0 {
				return 0
			}
			return math.Min(float64(raw%1_000_000)+0.01, maximumPolicyCostUSD)
		}
		base := ExecutionPolicyRule{
			Denied: baseDenied, Egress: egress(baseEgressCode), AllowedProviders: list(baseMask),
			AllowedGroups: list(baseMask >> 1), RequireCostBudget: baseRequire, MaxCostUSD: cost(baseCostRaw),
		}
		tenant := ExecutionPolicyRule{
			Denied: tenantDenied, Egress: egress(tenantEgressCode), AllowedProviders: list(tenantMask),
			AllowedGroups: list(tenantMask >> 1), RequireCostBudget: tenantRequire, MaxCostUSD: cost(tenantCostRaw),
		}
		merged := mergeExecutionPolicyRules(base, tenant)

		if base.Denied && !merged.Denied {
			t.Fatal("tenant rule removed the default deny boundary")
		}
		if base.RequireCostBudget && !merged.RequireCostBudget {
			t.Fatal("tenant rule removed the default cost-budget requirement")
		}
		if base.MaxCostUSD > 0 && (merged.MaxCostUSD <= 0 || merged.MaxCostUSD > base.MaxCostUSD) {
			t.Fatalf("tenant rule raised the default cost ceiling: base=%f merged=%f", base.MaxCostUSD, merged.MaxCostUSD)
		}
		for _, provider := range merged.AllowedProviders {
			if len(base.AllowedProviders) > 0 && !containsFold(base.AllowedProviders, provider) {
				t.Fatalf("tenant rule introduced provider %q outside the default allow-list", provider)
			}
		}
		for _, group := range merged.AllowedGroups {
			if len(base.AllowedGroups) > 0 && !containsFold(base.AllowedGroups, group) {
				t.Fatalf("tenant rule introduced group %q outside the default allow-list", group)
			}
		}
		if (base.Egress == "local_only" || base.Egress == "remote_only") && merged.Egress != base.Egress && !merged.Denied {
			t.Fatalf("tenant rule broadened egress from %q to %q without denying the intersection", base.Egress, merged.Egress)
		}
	})
}
