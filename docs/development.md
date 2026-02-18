# Development Guide

## Go Driver Development

### Prerequisites

- Go 1.24+
- Access to a Rancher v2.11.x instance for testing

### Building

```bash
cd driver/

# Build for local testing (macOS)
make build

# Build Linux binary for Rancher
make build-linux

# Or manually:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=$(git describe --tags --always)" \
  -o docker-machine-driver-hetzner \
  ./cmd/docker-machine-driver-hetzner
```

### Testing Locally

You can test the driver binary directly:

```bash
./docker-machine-driver-hetzner --version
```

### Packaging and Deploying a New Version

**Step 1: Build and package**

```bash
cd driver/
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=1.0.2" \
  -o docker-machine-driver-hetzner \
  ./cmd/docker-machine-driver-hetzner

tar czf docker-machine-driver-hetzner.tar.gz docker-machine-driver-hetzner
```

**Step 2: Upload to your hosting (S3, Hetzner Object Storage, etc.)**

```bash
aws s3 cp docker-machine-driver-hetzner.tar.gz \
  s3://your-bucket/docker-machine-driver-hetzner.tar.gz \
  --endpoint-url https://your-endpoint
```

**Step 3: Force Rancher to re-download the binary**

Rancher caches the driver binary internally and serves it to provisioning pods
from `/assets/`. Simply uploading a new file to S3 is not enough — Rancher must
be told to re-download it.

Option A — Change the URL (recommended):

```bash
# Bump the version query parameter to bust the cache
kubectl patch nodedriver.management.cattle.io/hetzner --type=merge -p '{
  "spec": {
    "active": false
  }
}'

sleep 3

kubectl patch nodedriver.management.cattle.io/hetzner --type=merge -p '{
  "spec": {
    "url": "https://your-bucket/docker-machine-driver-hetzner.tar.gz?v=NEW_VERSION",
    "active": true
  },
  "status": {
    "appliedURL": ""
  }
}'
```

Option B — Deactivate/reactivate cycle:

```bash
# Deactivate
kubectl patch nodedriver.management.cattle.io/hetzner --type=merge \
  -p '{"spec":{"active":false}}'

sleep 3

# Clear applied URL and reactivate
kubectl patch nodedriver.management.cattle.io/hetzner --type=merge \
  -p '{"spec":{"active":true},"status":{"appliedURL":""}}'
```

**Step 4: Verify the new binary is in use**

```bash
# Check the driver was re-downloaded (look for fresh timestamp)
kubectl get nodedriver.management.cattle.io/hetzner -o yaml | grep -A2 Downloaded

# Create a test cluster, then check the provisioning pod logs:
kubectl logs -n fleet-default <machine-provision-pod> | head -10
# The BuildID in the `file` output should differ from the previous version
```

**Note:** Existing clusters are not affected by driver updates. Only new machine
provisioning jobs will use the updated binary.

## UI Extension Development

### Prerequisites

- Node.js 18+ (tested with v23.7)
- Yarn 1.x

### Setup

```bash
cd extension/
yarn install
```

### Development Server

```bash
API=https://your-rancher-url yarn dev
```

Opens at `https://localhost:8005/`. Log in with your Rancher credentials.
Changes to Vue components hot-reload automatically. Changes to `index.js`
(store registration) require a server restart.

### Key Conventions

**Component filenames must match driver name:**
- `cloud-credential/hetzner.vue` (not `Hetzner.vue` or `hetzner-cloud.vue`)
- `machine-config/hetzner.vue`

**Store module must be registered explicitly** in `index.js` via
`plugin.addStore('hetzner', ...)`. It is NOT auto-discovered.

**API proxy auth header format:**
```
x-api-cattleauth-header: Bearer credID={id} passwordField=apiToken
```
The `passwordField` value must match the credential field name exactly.

### Building for Production

```bash
cd extension/
yarn build-pkg hetzner-node-driver
```

Output is in `dist-pkg/`. This can be served as a Rancher UIPlugin.
