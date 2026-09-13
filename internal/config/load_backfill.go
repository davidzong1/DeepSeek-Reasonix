package config

import (
	"net/url"
	"path/filepath"
	"reasonix/internal/provider"
	"strings"
)

// backfillDeepSeekPro restores deepseek-pro for configs the pre-fix setup wizard
// wrote with only deepseek-v4-flash: a keyless /models probe used to drop the Pro
// SKU, leaving users unable to switch to it. In-memory only — the user's file is
// untouched. Narrowly scoped to the official DeepSeek endpoint (which is known to
// serve pro) so a custom flash-only deployment isn't given an entry that 404s.
func backfillDeepSeekPro(c *Config) {
	const flashModel, proModel = "deepseek-v4-flash", "deepseek-v4-pro"
	var flash *ProviderEntry
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Name == "deepseek-pro" {
			return
		}
		for _, m := range p.ModelList() {
			switch m {
			case proModel:
				return // pro already reachable
			case flashModel:
				if strings.Contains(p.BaseURL, "api.deepseek.com") {
					flash = p
				}
			}
		}
	}
	if flash == nil {
		return
	}
	// If the user has explicitly curated a model list for the flash provider
	// (e.g. unchecked pro in Settings), respect that choice and do not backfill.
	if len(flash.Models) > 0 {
		return
	}
	for _, bp := range Default().Providers {
		if bp.Name == "deepseek-pro" {
			bp.APIKeyEnv = flash.APIKeyEnv
			// Inherit the flash provider's frozen billing currency for list prices.
			currency := flash.ProviderBillingCurrency()
			if currency == "" {
				currency = flash.persistedOfficialCurrency
			}
			if currency == "" {
				currency = "USD"
			}
			bp.BillingCurrency = currency
			bp.persistedOfficialCurrency = currency
			bp.Price = deepSeekV4PriceForModel(currency, proModel)
			c.Providers = append(c.Providers, bp)
			return
		}
	}
}

func backfillDeepSeekOfficialPrices(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if officialProviderKind(p) != "deepseek" {
			continue
		}
		backfillDeepSeekOfficialEndpointDefaults(p)
		currency := p.ProviderBillingCurrency()
		if currency == "" {
			currency = p.persistedOfficialCurrency
		}
		if currency == "" {
			currency = "USD"
		}
		defaults := DeepSeekV4PricesForCurrency(currency)
		if p.Price != nil {
			continue
		}
		if p.Prices == nil {
			p.Prices = map[string]*provider.Pricing{}
		}
		for model, price := range defaults {
			if p.HasModel(model) && p.Prices[model] == nil {
				p.Prices[model] = clonePricing(price)
			}
		}
	}
}

// backfillDeepSeekOfficialEndpointDefaults restores the two official-endpoint
// fields a config may legitimately omit. Both are safe to infer here precisely
// because the caller already matched api.deepseek.com: the wallet endpoint is
// the vendor's own, and 1M is that vendor's real window. Values the file
// declares are never overwritten.
//
// This is keyed on the endpoint rather than on list position, so it cannot leak
// onto a custom provider the way the previous positional decode overlay did
// (#7357, #7358).
func backfillDeepSeekOfficialEndpointDefaults(p *ProviderEntry) {
	if p == nil {
		return
	}
	if strings.TrimSpace(p.BalanceURL) == "" {
		p.BalanceURL = "https://api.deepseek.com/user/balance"
	}
	backfillOfficialContextWindow(p, 1_000_000)
}

func officialProviderKind(p *ProviderEntry) string {
	if p == nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Hostname(), "api.deepseek.com") {
		return "deepseek"
	}
	return ""
}

func resolveRoot(root string) string {
	if root == "" || root == "." {
		return "."
	}
	return filepath.Clean(root)
}
