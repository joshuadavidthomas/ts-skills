set unstable := true

# List all available commands
[private]
default:
    @just --list --list-submodules

# Build both binaries
build *ARGS:
    go build {{ ARGS }} -o ts-skills ./cmd/ts-skills
    go build {{ ARGS }} -o ts-skillsd ./cmd/ts-skillsd

# Run tests with coverage
coverage *ARGS:
    go test ./... -race -cover {{ ARGS }}

# Format code
fmt *ARGS='.':
    gofmt -w {{ ARGS }}

# Run all pre-commit hooks
lint:
    pre-commit run --all-files

# Run the daemon
run *ARGS:
    go run ./cmd/ts-skillsd {{ ARGS }}

# Rebuild the committed Tailwind CSS
css:
    npm run build:css

# Run tests
test *ARGS:
    go test ./... -race {{ ARGS }}

# Run static analysis
vet:
    go vet ./...

# Check dependencies for known vulnerabilities
vuln:
    govulncheck ./...

# Run all checks
check: test lint vet vuln tidy

# Tidy go.mod/go.sum
tidy:
    go mod tidy
