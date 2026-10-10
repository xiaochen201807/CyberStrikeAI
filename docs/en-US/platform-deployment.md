# Platform deployment boundaries

## Decision

GitHub is the source, CI, and image registry surface. It is not the long-running host for CyberStrikeAI. GitHub Actions now builds the control image, the fixed Nuclei/ZAP adapters, and eight verified family runtime images; GHCR stores images produced by the tag-based publish workflow. A Docker host, VM, or cluster still runs the control plane and scanners.

Cloudflare Workers/Pages are not a drop-in host for the current Go plus Docker Compose stack. The supported Cloudflare topology is a Docker VM behind Cloudflare Tunnel and Access. Cloudflare Containers can run Linux/amd64 images, but using it here requires a Worker/Durable Object wrapper, lifecycle and persistence design, and per-family validation for scanner networking and privileges; the repository does not claim that direct `wrangler deploy` support yet.

## Start on a Docker host

The current release target is a single `linux/amd64` Docker VM. Some family
installers use upstream `x86_64` binaries; ARM hosts require Docker amd64
emulation until the installers are validated natively.

```bash
git clone https://github.com/xiaochen201807/CyberStrikeAI.git
cd CyberStrikeAI
cp deploy/tools/.env.example deploy/tools/.env
openssl rand -hex 32
# Put the generated value in TOOL_MCP_TOKEN and set TOOL_ALLOWED_HOSTS.
./deploy/start-stack.sh
```

The helper validates the MCP token and target allowlist, starts the control
Compose project first (creating the shared network), starts `runtime-recon` and
`runtime-web`, and waits for `/readyz` plus each selected family's
`tool-mcp -probe`. Select more families with `RUNTIME_PROFILES`, or set
`BUILD_IMAGES=0` after filling the `.env` file with immutable GHCR image
references. Run `./deploy/check-stack.sh` later to repeat the health check.

Before an upgrade, create a consistent single-VM backup:

```bash
./deploy/backup-stack.sh
./deploy/restore-stack.sh ./backups/20260101T000000Z
```

The restore command leaves the stack stopped for review before running
`./deploy/start-stack.sh`.

## Publish from GitHub

```bash
git tag v0.1.0
git push origin v0.1.0
```

The publish workflow pushes the control image, all family images, and the fixed adapters to GHCR with SBOM and provenance metadata. On the runtime host, log in to `ghcr.io`, set `CYBERSTRIKE_IMAGE` and the selected family image variables in `deploy/tools/.env`, then run:

```bash
BUILD_IMAGES=0 ./deploy/start-stack.sh
```

## Put Cloudflare in front

Run the Compose stack on a VM and bind the control port to localhost:

```bash
CYBERSTRIKE_BIND=127.0.0.1 ./deploy/start-stack.sh
```

Run `cloudflared` on that VM and route a managed tunnel to `http://127.0.0.1:8080`. Protect the hostname with Cloudflare Access. Keep family MCP ports private; target traffic originates from the VM's egress network.

See the [Chinese platform guide](../zh-CN/platform-deployment.md) for the full operational checklist.
