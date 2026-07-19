[private]
default:
    @just --list

# Build the agtlog binary into ./agtlog.
build:
    CGO_ENABLED=0 go build -o agtlog ./cmd/agtlog

# Run all tests, including the no-leak guard.
test:
    go test ./...

# Run tests under the race detector separately because it requires cgo.
test-race:
    CGO_ENABLED=1 go test -race ./...

# Run static checks; golangci-lint remains advisory.
check:
    @u="$(gofmt -l cmd internal)"; if [ -n "$u" ]; then echo "gofmt needed:"; echo "$u"; exit 1; fi
    go vet ./...
    golangci-lint run ./... || true

# Scan committable files for machine-local identifiers.
leakcheck:
    go test ./internal/leakcheck/

# Run the commit gate installed by the flake dev shell.
pre-commit: build
    @u="$(gofmt -l cmd internal)"; if [ -n "$u" ]; then echo "gofmt needed:"; echo "$u"; exit 1; fi
    go vet ./...
    go test ./...

# Verify the Nix package independently from the fast commit gate.
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
