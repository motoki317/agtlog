package cost

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestEmbeddedTableContainsSupportedModels(t *testing.T) {
	table, err := EmbeddedTable()
	if err != nil {
		t.Fatalf("EmbeddedTable() error = %v", err)
	}

	models := []string{"claude-opus-4-8", "claude-fable-5", "claude-sonnet-5", "gpt-5.6", "gpt-5.6-sol"}
	for _, name := range models {
		if _, ok := table[name]; !ok {
			t.Errorf("EmbeddedTable() missing %q", name)
		}
	}
}

func TestRuntimePricingTableAcceptsEmbeddedSnapshot(t *testing.T) {
	table, err := runtimePricingTable(embeddedPricing)
	if err != nil {
		t.Fatalf("runtimePricingTable(embeddedPricing) error = %v", err)
	}
	if got := len(table); got < 1_000 {
		t.Fatalf("runtimePricingTable(embeddedPricing) models = %d, want at least 1000", got)
	}
}

func TestRuntimeTableOverlaysCachedModels(t *testing.T) {
	cacheDir := t.TempDir()
	cache := `{"claude-opus-4-8":{"input_cost_per_token":42}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "pricing.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}

	table, err := RuntimeTable(cacheDir, true)
	if err != nil {
		t.Fatalf("RuntimeTable() error = %v", err)
	}
	if got := table["claude-opus-4-8"].Input; got != 42 {
		t.Fatalf("cached input rate = %v, want 42", got)
	}
	if _, ok := table["gpt-5.6"]; !ok {
		t.Fatal("embedded-only gpt-5.6 pricing was removed by overlay")
	}
}

func TestRefreshTableOverlaysFetchedModelsAndStoresPayload(t *testing.T) {
	cacheDir := t.TempDir()
	payload := []byte(`{"runtime-model":{"input_cost_per_token":7}}`)

	table, err := refreshTable(runtimePricingOptions{
		ctx:      context.Background(),
		cacheDir: cacheDir,
		fetch: func(context.Context) ([]byte, error) {
			return payload, nil
		},
	})
	if err != nil {
		t.Fatalf("refreshTable() error = %v", err)
	}
	if got := table["runtime-model"].Input; got != 7 {
		t.Fatalf("runtime model input rate = %v, want 7", got)
	}
	if _, ok := table["gpt-5.6"]; !ok {
		t.Fatal("embedded-only gpt-5.6 pricing was removed by overlay")
	}
	cached, err := os.ReadFile(filepath.Join(cacheDir, pricingCacheName))
	if err != nil {
		t.Fatalf("read refreshed cache: %v", err)
	}
	if !bytes.Equal(cached, payload) {
		t.Fatalf("cached pricing = %q, want %q", cached, payload)
	}
}

func TestRefreshTableRejectsEmptyCacheDirectory(t *testing.T) {
	if _, err := RefreshTable(context.Background(), ""); err == nil {
		t.Fatal("RefreshTable() error = nil, want empty cache directory error")
	}
}

func TestRefreshTableRejectsEmptyCacheDirectoryBeforeFetch(t *testing.T) {
	fetches := 0

	_, err := refreshTable(runtimePricingOptions{
		ctx: context.Background(),
		fetch: func(context.Context) ([]byte, error) {
			fetches++
			return []byte(`{"runtime-model":{"input_cost_per_token":7}}`), nil
		},
	})
	if err == nil {
		t.Fatal("refreshTable() error = nil, want empty cache directory error")
	}
	if fetches != 0 {
		t.Fatalf("fetches = %d, want 0", fetches)
	}
}

func TestRefreshTableFetchErrorLeavesExistingCacheUntouched(t *testing.T) {
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, pricingCacheName)
	existing := []byte(`{"cached-model":{"input_cost_per_token":3}}`)
	if err := os.WriteFile(cachePath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	fetchErr := errors.New("pricing service unavailable")

	_, err := refreshTable(runtimePricingOptions{
		ctx:      context.Background(),
		cacheDir: cacheDir,
		fetch: func(context.Context) ([]byte, error) {
			return nil, fetchErr
		},
	})
	if !errors.Is(err, fetchErr) {
		t.Fatalf("refreshTable() error = %v, want %v", err, fetchErr)
	}
	cached, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, existing) {
		t.Fatalf("cached pricing = %q, want %q", cached, existing)
	}
}

func TestRefreshTableInvalidPayloadLeavesExistingCacheUntouched(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "malformed", payload: []byte(`{malformed`)},
		{name: "unpriced", payload: []byte(`{"runtime-model":{"max_input_tokens":1000}}`)},
		{name: "null input rate", payload: []byte(`{"runtime-model":{"input_cost_per_token":null}}`)},
		{name: "null output rate", payload: []byte(`{"runtime-model":{"output_cost_per_token":null}}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			cachePath := filepath.Join(cacheDir, pricingCacheName)
			existing := []byte(`{"cached-model":{"input_cost_per_token":3}}`)
			if err := os.WriteFile(cachePath, existing, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := refreshTable(runtimePricingOptions{
				ctx:      context.Background(),
				cacheDir: cacheDir,
				fetch: func(context.Context) ([]byte, error) {
					return test.payload, nil
				},
			})
			if err == nil {
				t.Fatal("refreshTable() error = nil, want invalid payload error")
			}
			cached, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cached, existing) {
				t.Fatalf("cached pricing = %q, want %q", cached, existing)
			}
		})
	}
}

func TestRefreshTableReturnsStoreFailure(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache-file")
	if err := os.WriteFile(cacheDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := refreshTable(runtimePricingOptions{
		ctx:      context.Background(),
		cacheDir: cacheDir,
		fetch: func(context.Context) ([]byte, error) {
			return []byte(`{"runtime-model":{"input_cost_per_token":7}}`), nil
		},
	})
	if err == nil {
		t.Fatal("refreshTable() error = nil, want store error")
	}
}

func TestRefreshTableReturnsCancelledFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cacheDir := t.TempDir()

	_, err := refreshTable(runtimePricingOptions{
		ctx:      ctx,
		cacheDir: cacheDir,
		fetch: func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("refreshTable() error = %v, want %v", err, context.Canceled)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, pricingCacheName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pricing cache stat error = %v, want not exist", err)
	}
}

func TestRefreshTableGivesFetchThirtySecondDeadline(t *testing.T) {
	started := time.Now()
	var deadline time.Time

	_, err := refreshTable(runtimePricingOptions{
		ctx:      context.Background(),
		cacheDir: t.TempDir(),
		fetch: func(ctx context.Context) ([]byte, error) {
			var ok bool
			deadline, ok = ctx.Deadline()
			if !ok {
				return nil, errors.New("fetch context has no deadline")
			}
			return []byte(`{"runtime-model":{"input_cost_per_token":7}}`), nil
		},
	})
	if err != nil {
		t.Fatalf("refreshTable() error = %v", err)
	}
	if remaining := deadline.Sub(started); remaining < 29*time.Second || remaining > 31*time.Second {
		t.Fatalf("fetch deadline after %v, want about 30s", remaining)
	}
}

func TestRuntimePricingTableIgnoresUnusedMaxInputTokens(t *testing.T) {
	table, err := runtimePricingTable([]byte(`{
		"runtime-model":{"input_cost_per_token":0.000001,"max_input_tokens":"not runtime pricing"}
	}`))
	if err != nil {
		t.Fatalf("runtimePricingTable() error = %v", err)
	}
	if _, ok := table["runtime-model"]; !ok {
		t.Fatal("runtimePricingTable() removed priced model with unused max_input_tokens metadata")
	}
}

func TestRuntimePricingTableSkipsLiteLLMSampleSpec(t *testing.T) {
	payload := []byte(`{
		"sample_spec":{"input_cost_per_token":0,"max_input_tokens":"maximum supported input"},
		"runtime-model":{"input_cost_per_token":0.000001}
	}`)

	table, err := runtimePricingTable(payload)
	if err != nil {
		t.Fatalf("runtimePricingTable() error = %v", err)
	}
	if _, ok := table["sample_spec"]; ok {
		t.Fatal("runtimePricingTable() included LiteLLM sample spec")
	}
	if _, ok := table["runtime-model"]; !ok {
		t.Fatal("runtimePricingTable() removed valid priced model")
	}
}

func TestPricingCacheRefreshDecision(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name    string
		age     time.Duration
		missing bool
		want    bool
	}{
		{name: "fresh", age: time.Hour, want: false},
		{name: "old", age: 25 * time.Hour, want: true},
		{name: "future", age: -time.Hour, want: true},
		{name: "missing", missing: true, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pricing.json")
			if !test.missing {
				if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				modified := now.Add(-test.age)
				if err := os.Chtimes(path, modified, modified); err != nil {
					t.Fatal(err)
				}
			}
			if got := pricingCacheNeedsRefresh(path, now); got != test.want {
				t.Fatalf("pricingCacheNeedsRefresh() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRuntimeTableRefreshScheduling(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name      string
		cacheAge  time.Duration
		cacheFile bool
		wantFetch int
	}{
		{name: "fresh", cacheAge: time.Hour, cacheFile: true, wantFetch: 0},
		{name: "old", cacheAge: 25 * time.Hour, cacheFile: true, wantFetch: 1},
		{name: "missing", wantFetch: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			cachePath := filepath.Join(cacheDir, "pricing.json")
			if test.cacheFile {
				if err := os.WriteFile(cachePath, []byte(`{"cached-model":{"input_cost_per_token":1}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				modified := now.Add(-test.cacheAge)
				if err := os.Chtimes(cachePath, modified, modified); err != nil {
					t.Fatal(err)
				}
			}
			fetches := 0
			table, err := runtimeTable(runtimePricingOptions{
				cacheDir: cacheDir,
				now:      func() time.Time { return now },
				fetch: func(context.Context) ([]byte, error) {
					fetches++
					return []byte(`{"runtime-model":{"input_cost_per_token":7}}`), nil
				},
				start: func(task func()) { task() },
			})
			if err != nil {
				t.Fatalf("runtimeTable() error = %v", err)
			}
			if fetches != test.wantFetch {
				t.Fatalf("fetches = %d, want %d", fetches, test.wantFetch)
			}
			if test.wantFetch == 0 {
				return
			}
			if _, ok := table["runtime-model"]; ok {
				t.Fatal("fresh pricing was hot-swapped into the startup table")
			}
			cached, err := os.ReadFile(cachePath)
			if err != nil {
				t.Fatalf("read refreshed cache: %v", err)
			}
			parsed, err := pricingTable(cached)
			if err != nil || parsed["runtime-model"].Input != 7 {
				t.Fatalf("refreshed cache = %#v, %v", parsed, err)
			}
		})
	}
}

func TestRuntimeTableOfflineSkipsFetch(t *testing.T) {
	fetches := 0
	_, err := runtimeTable(runtimePricingOptions{
		cacheDir: t.TempDir(),
		offline:  true,
		fetch: func(context.Context) ([]byte, error) {
			fetches++
			return []byte("{}"), nil
		},
		start: func(task func()) { task() },
	})
	if err != nil {
		t.Fatalf("runtimeTable() error = %v", err)
	}
	if fetches != 0 {
		t.Fatalf("offline fetches = %d, want 0", fetches)
	}
}

func TestRuntimeTableMalformedCacheFallsBackToEmbedded(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, pricingCacheName), []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}

	table, err := runtimeTable(runtimePricingOptions{cacheDir: cacheDir, offline: true})
	if err != nil {
		t.Fatalf("runtimeTable() error = %v", err)
	}
	if _, ok := table["gpt-5.6"]; !ok {
		t.Fatal("malformed cache removed embedded gpt-5.6 pricing")
	}
}

func TestRuntimeTableRejectsUnpricedCachedEntries(t *testing.T) {
	embedded, err := EmbeddedTable()
	if err != nil {
		t.Fatal(err)
	}
	want := embedded["gpt-5.6"]
	tests := []struct {
		name    string
		payload string
	}{
		{name: "null", payload: `{"gpt-5.6":null}`},
		{name: "empty", payload: `{"gpt-5.6":{}}`},
		{name: "negative", payload: `{"gpt-5.6":{"input_cost_per_token":-1}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(cacheDir, pricingCacheName), []byte(test.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			table, err := RuntimeTable(cacheDir, true)
			if err != nil {
				t.Fatalf("RuntimeTable() error = %v", err)
			}
			if got := table["gpt-5.6"]; got.Input != want.Input || got.Output != want.Output {
				t.Fatalf("gpt-5.6 pricing = %#v, want embedded %#v", got, want)
			}
		})
	}
}

func TestReadPricingCacheSupportsParseableXDGFiles(t *testing.T) {
	t.Run("regular", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), pricingCacheName)
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPricingCache(path); err != nil {
			t.Fatalf("readPricingCache() error = %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), pricingCacheName)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxPricingBytes + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := readPricingCache(path); err == nil {
			t.Fatal("readPricingCache() accepted oversized cache")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, pricingCacheName)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := readPricingCache(path); err != nil {
			t.Fatalf("readPricingCache() error = %v", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), pricingCacheName)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := readPricingCache(path); err == nil {
			t.Fatal("readPricingCache() accepted directory")
		}
	})
	t.Run("world writable", func(t *testing.T) {
		cacheDir := t.TempDir()
		if err := os.Chmod(cacheDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(cacheDir, pricingCacheName)
		if err := os.WriteFile(path, []byte(`{"gpt-5.6":{"input_cost_per_token":42}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		table, err := RuntimeTable(cacheDir, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := table["gpt-5.6"].Input; got != 42 {
			t.Fatalf("cached input rate = %v, want 42", got)
		}
	})
}

func TestStorePricingCacheSupportsXDGDirectoryLayouts(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		target := t.TempDir()
		cacheDir := filepath.Join(parent, "agtlog")
		if err := os.Symlink(target, cacheDir); err != nil {
			t.Fatal(err)
		}
		if err := storePricingCache(cacheDir, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(target, pricingCacheName)); err != nil {
			t.Fatalf("symlink target pricing file error = %v", err)
		}
	})
	t.Run("group writable", func(t *testing.T) {
		cacheDir := t.TempDir()
		if err := os.Chmod(cacheDir, 0o770); err != nil {
			t.Fatal(err)
		}
		if err := storePricingCache(cacheDir, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("symlink ancestor", func(t *testing.T) {
		parent := t.TempDir()
		target := t.TempDir()
		linkedParent := filepath.Join(parent, "cache")
		if err := os.Symlink(target, linkedParent); err != nil {
			t.Fatal(err)
		}
		cacheDir := filepath.Join(linkedParent, "agtlog")
		if err := storePricingCache(cacheDir, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(target, "agtlog", pricingCacheName)); err != nil {
			t.Fatalf("symlink ancestor target error = %v", err)
		}
	})
}

func TestReadPricingResponseEnforcesLimit(t *testing.T) {
	if got, err := readPricingResponse(bytes.NewBufferString("1234"), 4); err != nil || string(got) != "1234" {
		t.Fatalf("exact-limit response = %q, %v", got, err)
	}
	if _, err := readPricingResponse(bytes.NewBufferString("12345"), 4); err == nil {
		t.Fatal("readPricingResponse() accepted response over limit")
	}
}

func TestFetchPricingRejectsNonSuccessStatus(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Status:     "503 Service Unavailable",
				Body:       http.NoBody,
				Request:    request,
			}, nil
		}),
	}

	if _, err := fetchPricing(context.Background(), client); err == nil || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("fetchPricing() error = %v, want non-success status", err)
	}
}

func TestTableResolveUsesProviderQualifiedModel(t *testing.T) {
	table := Table{"anthropic.model-a": {Input: 1}}

	key, pricing, ok := table.Resolve("model-a")
	if !ok || key != "anthropic.model-a" || pricing.Input != 1 {
		t.Fatalf("Resolve() = %q, %#v, %v", key, pricing, ok)
	}
}

func TestCodexResolutionUsesOwnPublishedSolRate(t *testing.T) {
	table := Table{
		"gpt-5.6":     {Input: 2},
		"gpt-5.6-sol": {Input: 3},
	}

	key, pricing, _, ok := table.ResolveCodex("gpt-5.6-sol", "gpt-5")
	if !ok || key != "gpt-5.6-sol" || pricing.Input != 3 {
		t.Fatalf("ResolveCodex() = %q, %#v, %v", key, pricing, ok)
	}
}

func TestCodexResolutionReportsExactness(t *testing.T) {
	table := Table{
		"gpt-5":          {Input: 1},
		"gpt-5.7":        {Input: 2},
		"gpt-5.6-sol":    {Input: 3},
		"openai/gpt-5.5": {Input: 4},
		"gpt-5.3-codex":  {Input: 5},
		"gpt-5.6-luna":   {Input: 6},
		"openai/gpt-5.8": {Input: 7},
	}
	tests := []struct {
		name      string
		model     string
		wantKey   string
		wantExact bool
	}{
		{name: "sol entry", model: "gpt-5.6-sol", wantKey: "gpt-5.6-sol", wantExact: true},
		{name: "provider-prefixed entry", model: "gpt-5.5", wantKey: "openai/gpt-5.5", wantExact: true},
		{name: "codex entry", model: "gpt-5.3-codex", wantKey: "gpt-5.3-codex", wantExact: true},
		{name: "luna entry", model: "gpt-5.6-luna", wantKey: "gpt-5.6-luna", wantExact: true},
		{name: "suffix fallback", model: "gpt-5.7-sol", wantKey: "gpt-5.7", wantExact: false},
		{name: "provider suffix fallback", model: "openai/gpt-5.8-sol", wantKey: "openai/gpt-5.8", wantExact: false},
		{name: "default fallback", model: "future-model", wantKey: "gpt-5", wantExact: false},
		{name: "empty model", wantKey: "gpt-5", wantExact: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, _, exact, ok := table.ResolveCodex(test.model, "gpt-5")
			if !ok || key != test.wantKey || exact != test.wantExact {
				t.Fatalf("ResolveCodex(%q) = %q, exact %t, ok %t; want %q, exact %t, ok true",
					test.model, key, exact, ok, test.wantKey, test.wantExact)
			}
		})
	}
}

func TestCodexResolutionKeepsPublicGPT5Models(t *testing.T) {
	table := Table{"gpt-5.4": {Input: 2}}

	key, _, _, ok := table.ResolveCodex("gpt-5.4", "gpt-5")
	if !ok || key != "gpt-5.4" {
		t.Fatalf("ResolveCodex() = %q, _, %v, want public model", key, ok)
	}
}

func TestCodexResolutionStripsPrivateGPT5Variant(t *testing.T) {
	table := Table{"gpt-5.4": {Input: 2}}

	key, _, _, ok := table.ResolveCodex("gpt-5.4-sol", "gpt-5")
	if !ok || key != "gpt-5.4" {
		t.Fatalf("ResolveCodex() = %q, _, %v, want base public model", key, ok)
	}
}

func TestCodexResolutionUsesConfiguredDefault(t *testing.T) {
	table := Table{"gpt-5": {Input: 2}}

	key, _, _, ok := table.ResolveCodex("future-codex-model", "gpt-5")
	if !ok || key != "gpt-5" {
		t.Fatalf("ResolveCodex() = %q, _, %v, want configured default", key, ok)
	}
}
