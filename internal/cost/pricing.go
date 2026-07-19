package cost

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
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
	cacheDir string
	offline  bool
	now      func() time.Time
	fetch    func(context.Context) ([]byte, error)
	start    func(func())
}

func EmbeddedTable() (Table, error) {
	return pricingTable(embeddedPricing)
}

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
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil || fields == nil {
			if strict {
				return nil, fmt.Errorf("invalid pricing entry %q", name)
			}
			continue
		}
		_, hasInput := fields["input_cost_per_token"]
		_, hasOutput := fields["output_cost_per_token"]
		if !hasInput && !hasOutput {
			continue
		}
		var pricing Pricing
		if err := json.Unmarshal(entry, &pricing); err != nil || !validPricing(pricing) {
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

func validPricing(pricing Pricing) bool {
	if !validRate(pricing.Input) || !validRate(pricing.Output) || pricing.MaxInputTokens < 0 || !validRate(pricing.ProviderSpecificEntry.Fast) {
		return false
	}
	for _, rate := range []*float64{
		pricing.CacheWrite, pricing.CacheRead,
		pricing.InputAbove200K, pricing.OutputAbove200K, pricing.CacheWriteAbove200K, pricing.CacheReadAbove200K,
		pricing.InputAbove272K, pricing.OutputAbove272K, pricing.CacheWriteAbove272K, pricing.CacheReadAbove272K,
	} {
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

func (t Table) ResolveCodex(modelName, defaultModel string) (string, Pricing, bool) {
	if modelName == "gpt-5.6-sol" {
		// The -sol slug is a Codex runtime variant; gpt-5.6 is the nearest
		// public API price and keeps the resulting subscription cost estimated.
		return t.Resolve("gpt-5.6")
	}
	if strings.HasPrefix(modelName, "gpt-5") {
		if key, pricing, ok := t.Resolve(modelName); ok {
			return key, pricing, true
		}
		for _, suffix := range []string{"-sol", "-terra", "-luna"} {
			if base, found := strings.CutSuffix(modelName, suffix); found {
				if key, pricing, ok := t.Resolve(base); ok {
					return key, pricing, true
				}
			}
		}
	}
	return t.Resolve(defaultModel)
}
