package cost

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed data/litellm-pricing.json
var embeddedPricing []byte

const (
	pricingCacheName = "pricing.json"
	pricingURL       = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	maxPricingBytes  = 64 << 20
)

type runtimePricingOptions struct {
	ctx      context.Context
	cacheDir string
	offline  bool
	now      func() time.Time
	fetch    func(context.Context) ([]byte, error)
	start    func(func())
}

func EmbeddedTable() (Table, error) {
	return pricingTable(embeddedPricing)
}

// RuntimeTable can start a background fetch that replaces the cache file after
// RuntimeTable returns. The returned table does not change.
func RuntimeTable(cacheDir string, offline bool) (Table, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	return runtimeTable(runtimePricingOptions{
		cacheDir: cacheDir,
		offline:  offline,
		now:      time.Now,
		fetch:    func(ctx context.Context) ([]byte, error) { return fetchPricing(ctx, client) },
		start:    func(task func()) { go task() },
	})
}

func RefreshTable(ctx context.Context, cacheDir string) (Table, error) {
	client := &http.Client{}
	return refreshTable(runtimePricingOptions{
		ctx:      ctx,
		cacheDir: cacheDir,
		now:      time.Now,
		fetch:    func(ctx context.Context) ([]byte, error) { return fetchPricing(ctx, client) },
	})
}

func runtimeTable(options runtimePricingOptions) (Table, error) {
	table, err := EmbeddedTable()
	if err != nil {
		return nil, err
	}
	if options.cacheDir == "" {
		return table, nil
	}
	cachePath := filepath.Join(options.cacheDir, pricingCacheName)
	cacheValid := false
	if data, readErr := readPricingCache(cachePath); readErr == nil {
		if cache, parseErr := runtimePricingTable(data); parseErr == nil {
			cacheValid = true
			for name, pricing := range cache {
				table[name] = pricing
			}
		}
	}
	if !options.offline && (!cacheValid || pricingCacheNeedsRefresh(cachePath, options.now())) {
		options.start(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			data, fetchErr := options.fetch(ctx)
			if fetchErr != nil {
				return
			}
			if _, parseErr := runtimePricingTable(data); parseErr != nil {
				return
			}
			_ = storePricingCache(options.cacheDir, data)
		})
	}
	return table, nil
}

func refreshTable(options runtimePricingOptions) (Table, error) {
	if options.cacheDir == "" {
		return nil, fmt.Errorf("pricing cache directory is empty")
	}
	table, err := EmbeddedTable()
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(options.ctx, 30*time.Second)
	defer cancel()
	data, err := options.fetch(fetchCtx)
	if err != nil {
		return nil, err
	}
	fetched, err := runtimePricingTable(data)
	if err != nil {
		return nil, err
	}
	for name, pricing := range fetched {
		table[name] = pricing
	}
	if err := storePricingCache(options.cacheDir, data); err != nil {
		return nil, err
	}
	return table, nil
}

func fetchPricing(ctx context.Context, client *http.Client) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pricingURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("pricing response status %s", response.Status)
	}
	return readPricingResponse(response.Body, maxPricingBytes)
}

func readPricingResponse(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("pricing response exceeds %d bytes", limit)
	}
	return data, nil
}

func readPricingCache(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || opened.Size() > maxPricingBytes {
		return nil, fmt.Errorf("pricing cache is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPricingBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPricingBytes {
		return nil, fmt.Errorf("pricing cache exceeds %d bytes", maxPricingBytes)
	}
	return data, nil
}

func storePricingCache(cacheDir string, data []byte) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(cacheDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("pricing cache path is not a directory")
	}
	temporary, err := os.CreateTemp(cacheDir, ".pricing-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(cacheDir, pricingCacheName))
}

func pricingTable(data []byte) (Table, error) {
	return decodePricingTable(data, false)
}

func runtimePricingTable(data []byte) (Table, error) {
	return decodePricingTable(data, true)
}

func decodePricingTable(data []byte, strict bool) (Table, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	table := make(Table, len(raw))
	for name, entry := range raw {
		// LiteLLM's sample_spec is a documentation template with
		// pricing-shaped keys, not a model.
		if name == "sample_spec" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil || fields == nil {
			if strict {
				return nil, fmt.Errorf("invalid pricing entry %q", name)
			}
			continue
		}
		inputRate, hasInput := fields["input_cost_per_token"]
		outputRate, hasOutput := fields["output_cost_per_token"]
		if !hasInput && !hasOutput {
			continue
		}
		if hasInput && bytes.Equal(bytes.TrimSpace(inputRate), []byte("null")) ||
			hasOutput && bytes.Equal(bytes.TrimSpace(outputRate), []byte("null")) {
			if strict {
				return nil, fmt.Errorf("invalid pricing rates for %q", name)
			}
			continue
		}
		var pricing Pricing
		err := json.Unmarshal(entry, &pricing)
		if err == nil {
			pricing.Tiers, err = decodeTiers(fields)
		}
		if err != nil || !validPricing(pricing) {
			if strict {
				return nil, fmt.Errorf("invalid pricing rates for %q", name)
			}
			continue
		}
		table[name] = pricing
	}
	if strict && len(table) == 0 {
		return nil, fmt.Errorf("pricing table has no token-priced models")
	}
	return table, nil
}

// decodeTiers reads LiteLLM's input_cost_per_token_above_<N>_tokens family.
// The input key defines a tier, and the other rates must spell the same <N>.
// Exact keys leave out variants with a further suffix, such as _priority,
// which price another service tier.
func decodeTiers(fields map[string]json.RawMessage) ([]PriceTier, error) {
	var tiers []PriceTier
	for key, raw := range fields {
		n, isInput := strings.CutPrefix(key, "input_cost_per_token_above_")
		n, isTokens := strings.CutSuffix(n, "_tokens")
		if !isInput || !isTokens {
			continue
		}
		threshold, valid := tierThreshold(n)
		if !valid {
			continue
		}
		var input *float64
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		if input == nil {
			continue
		}
		tier := PriceTier{Threshold: threshold, Input: *input}
		for prefix, rate := range map[string]**float64{
			"output_cost_per_token_above_":           &tier.Output,
			"cache_creation_input_token_cost_above_": &tier.CacheWrite,
			"cache_read_input_token_cost_above_":     &tier.CacheRead,
		} {
			if value, ok := fields[prefix+n+"_tokens"]; ok {
				if err := json.Unmarshal(value, rate); err != nil {
					return nil, err
				}
			}
		}
		tiers = append(tiers, tier)
	}
	slices.SortFunc(tiers, func(a, b PriceTier) int { return cmp.Compare(a.Threshold, b.Threshold) })
	// Two spellings of one threshold, such as 100k and 100000, would leave the
	// choice between their rates to map order.
	for index := 1; index < len(tiers); index++ {
		if tiers[index].Threshold == tiers[index-1].Threshold {
			return nil, fmt.Errorf("duplicate tier threshold %d", tiers[index].Threshold)
		}
	}
	return tiers, nil
}

// tierThreshold parses digits with an optional k for thousands. The 32-bit
// limit keeps N × 1000 inside int64.
func tierThreshold(n string) (int64, bool) {
	scale := uint64(1)
	if digits, ok := strings.CutSuffix(n, "k"); ok {
		n, scale = digits, 1000
	}
	value, err := strconv.ParseUint(n, 10, 32)
	return int64(value * scale), err == nil
}

func validPricing(pricing Pricing) bool {
	if !validRate(pricing.Input) || !validRate(pricing.Output) || !validRate(pricing.ProviderSpecificEntry.Fast) {
		return false
	}
	rates := []*float64{pricing.CacheWrite, pricing.CacheRead}
	for _, tier := range pricing.Tiers {
		rates = append(rates, &tier.Input, tier.Output, tier.CacheWrite, tier.CacheRead)
	}
	for _, rate := range rates {
		if rate != nil && !validRate(*rate) {
			return false
		}
	}
	return true
}

func validRate(rate float64) bool {
	return rate >= 0 && !math.IsInf(rate, 0) && !math.IsNaN(rate)
}

func pricingCacheNeedsRefresh(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	age := now.Sub(info.ModTime())
	return age < 0 || age > 24*time.Hour
}

func (t Table) Resolve(modelName string) (string, Pricing, bool) {
	for _, candidate := range []string{
		modelName,
		"anthropic." + modelName,
		"anthropic/" + modelName,
		"openai/" + modelName,
	} {
		if pricing, ok := t[candidate]; ok {
			return candidate, pricing, true
		}
	}
	return "", Pricing{}, false
}

// ResolveCodex reports exact only for the logged model's own published rate. A
// rate from a base model or from defaultModel is an estimate.
func (t Table) ResolveCodex(modelName, defaultModel string) (key string, exact, ok bool) {
	if key, _, ok := t.Resolve(modelName); ok {
		return key, true, true
	}
	codexModelName := strings.TrimPrefix(modelName, "openai/")
	if strings.HasPrefix(codexModelName, "gpt-5") {
		for _, suffix := range []string{"-sol", "-terra", "-luna"} {
			if base, found := strings.CutSuffix(modelName, suffix); found {
				if key, _, ok := t.Resolve(base); ok {
					return key, false, true
				}
			}
		}
	}
	key, _, ok = t.Resolve(defaultModel)
	return key, false, ok
}
