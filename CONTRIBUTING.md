# Contributing to Rancher Hetzner Cloud Provider

Thank you for your interest in contributing! This project provides a Hetzner Cloud machine driver and Rancher UI extension for provisioning RKE2 clusters.

## Getting Started

### Prerequisites

- **Go 1.24+** (machine driver)
- **Node 20 + Yarn** (UI extension)
- A Hetzner Cloud API token for integration testing

### Repository Layout

```
driver/     Go machine driver (hcloud-go, rancher/machine)
extension/  Vue 3 Rancher UI extension (@rancher/shell)
docs/       Architecture, development, and installation guides
```

See [docs/architecture.md](docs/architecture.md) for a detailed technical overview and [docs/development.md](docs/development.md) for build and test instructions.

## Development Workflow

### Building and Testing

```bash
# Driver
cd driver
make build        # local binary
make test         # run tests

# Extension
cd extension
yarn install
yarn build-pkg hetzner-node-driver
```

### Testing Notes

The Go driver tests mock the Hetzner API at the HTTP level using `httptest.NewServer` (the hcloud client is a concrete type, not an interface). When writing tests:

- Use `newTestDriver(t, mux)` to get a driver with a mock client
- Set `hcloud.WithRetryOpts(RetryOpts{MaxRetries: 0})` to avoid retry delays
- Set `hcloud.WithPollOpts(PollOpts{BackoffFunc: ConstantBackoff(0)})` for fast action polling
- Mock `/actions` returning `status: "success"` for action polling endpoints

### Branching Strategy

| Branch | Purpose | Release |
|--------|---------|---------|
| `feature/*` | New features and fixes | Pre-release on push |
| `develop` | Integration branch | Pre-release on push |
| `main` | Stable releases | Auto-incremented semver |

1. Create a feature branch from `develop`
2. Open a PR targeting `develop`
3. Once merged, `develop` gets a pre-release for testing
4. When ready, `develop` is merged to `main` for a stable release

## Submitting Changes

1. Fork the repository and create a feature branch from `develop`
2. Make your changes with clear, atomic commits
3. Ensure tests pass and the build is clean
4. Open a pull request against `develop`

### Code Standards

- **Go**: Follow standard Go conventions. CI runs `golangci-lint`
- **Vue**: Component filenames must match the driver name. Store modules must be explicitly registered (Rancher does not auto-discover them)
- **Firewall logic**: The concurrent read-modify-verify-retry pattern in `firewall.go` is sensitive to race conditions. Test thoroughly if modifying

### What Makes a Good PR

- Focused on a single concern (feature, bugfix, or refactor)
- Includes tests for new driver functionality
- Updates documentation if behavior changes
- Notes which component(s) are affected (driver, extension, or both)

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
