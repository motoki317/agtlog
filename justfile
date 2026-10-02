[private]
default:
    @just --list

# Build the agtlog binary into ./agtlog.
build:
    CGO_ENABLED=0 go build -o agtlog ./cmd/agtlog

# Run all tests, including the leak guard.
test: leakcheck
    go test ./...

# Run tests under the race detector, which needs cgo.
test-race:
    CGO_ENABLED=1 go test -race ./...

# Check gofmt and go vet, and run golangci-lint as advisory.
check:
    @u="$(gofmt -l cmd internal)"; if [ -n "$u" ]; then echo "gofmt needed:"; echo "$u"; exit 1; fi
    go vet ./...
    golangci-lint run ./... || true

# The guard reads files through git grep, which the Go test cache does not
# track. Without -count=1, a cached pass hides a leak in an edited file.

# Scan committable files for local identifiers.
leakcheck:
    go test -count=1 ./internal/leakcheck/

# Run the commit gate installed by the flake dev shell.
pre-commit: build leakcheck
    @u="$(gofmt -l cmd internal)"; if [ -n "$u" ]; then echo "gofmt needed:"; echo "$u"; exit 1; fi
    go vet ./...
    go test ./...

# Build the Nix package outside the fast commit gate.
nix-build:
    nix build .#agtlog --no-link --print-build-logs

# Refresh the embedded LiteLLM pricing snapshot atomically.
update-pricing:
    mkdir -p internal/cost/data
    curl --fail --location --silent --show-error https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json -o internal/cost/data/litellm-pricing.json.tmp
    mv internal/cost/data/litellm-pricing.json.tmp internal/cost/data/litellm-pricing.json

# Format Go sources.
fmt:
    go fmt ./...
