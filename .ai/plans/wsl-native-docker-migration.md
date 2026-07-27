# WSL stability and native Docker Engine migration

Status: completed for WSL/Docker recovery and cleanup; Compose secret configuration remains
Current phase: 3 - workload validation
Last verified: 2026-07-27
Next action: provide the required Compose secret before the next full Compose recreate

## Objective

Stabilize Ubuntu under WSL2 and migrate Docker workloads from Docker Desktop to Docker Engine running natively inside Ubuntu. Do not uninstall Docker Desktop, unregister Ubuntu, prune data, or delete VHDs until the acceptance gates below pass.

## Confirmed evidence

- `wsl.exe --version`: WSL `2.7.11.0`, kernel `6.1.18.3.2-2`.
- `wsl.exe -l -v`: Ubuntu running; `docker-desktop` stopped.
- Direct Ubuntu probes (`/bin/true`, `/bin/echo`) timed out before command execution; WSL client processes accumulated from repeated timed-out probes.
- Windows-side Docker CLI config selects `desktop-linux` and uses `credsStore=desktop`; the default named pipe daemon is unavailable.
- User `.wslconfig` has a 32 GB memory ceiling, 8 GB swap, mirrored networking, gradual reclaim, 5-minute VM idle timeout, and experimental sparse VHD behavior.
- No Docker or WSL process/service was terminated by this task.
- Docker Desktop 4.75.0 was uninstalled after independent removal-readiness review; no Desktop engine data was identified as required by native workloads.
- Docker build cache was pruned: 35.19 GB reclaimed. No images or volumes were pruned.
- Existing Postgres, Redis, and MinIO containers were started successfully; the automation runner remains in a restart loop.
- Compose rendering is blocked because `MIVIA_INTAKE_LOCAL_HTTP_SHARED_SECRET` is absent; no secret was fabricated.
- Restarted dependency services and restored the runner; runner, Mivia server, Temporal, Postgres, Redis, MinIO, and Memgraph are healthy.
- Removed dangling untagged images: 3.116 GB reclaimed. Tagged images and all volumes were preserved.

Primary references:

- [Microsoft WSL troubleshooting](https://learn.microsoft.com/en-us/windows/wsl/troubleshooting-guide)
- [Microsoft WSL basic commands](https://learn.microsoft.com/en-us/windows/wsl/basic-commands)
- [Microsoft WSL advanced configuration](https://learn.microsoft.com/en-us/windows/wsl/wsl-config)
- [Microsoft systemd in WSL](https://learn.microsoft.com/en-us/windows/wsl/systemd)
- [Microsoft WSL filesystem guidance](https://learn.microsoft.com/en-us/windows/wsl/filesystems)
- [Docker Engine on Ubuntu](https://docs.docker.com/engine/install/ubuntu/)
- [Docker daemon storage](https://docs.docker.com/engine/daemon/)
- [Docker Desktop backup and restore](https://docs.docker.com/desktop/settings-and-maintenance/backup-and-restore/)
- [Docker Desktop uninstall warning](https://docs.docker.com/desktop/uninstall/)
- [Docker volume backup and restore](https://docs.docker.com/engine/storage/volumes/)

## Scope

In scope: WSL command/session stability, Ubuntu systemd readiness, native rootful Docker Engine, Docker CLI context cleanup, Docker storage location, workload recreation, backup/restore validation, and eventual Desktop removal.

Out of scope: WSL distro replacement, `wsl --unregister`, arbitrary `.wslconfig` tuning, unauthenticated Docker TCP exposure, automatic `docker system prune`, rootless migration as the initial cutover, and application changes unrelated to container portability.

## Phase 0 - stability gate and inventory

### Read first

- Current `.wslconfig`.
- Windows Docker CLI config and contexts.
- WSL status/list output.
- Ubuntu `/etc/wsl.conf`, mounts, PID 1, disk and memory state, once Ubuntu responds.
- All Compose files, environment references, named volumes, bind mounts, ports, restart policies, and stateful services.

### Bounded checks

Run one WSL probe at a time. Use a caller-level timeout that also terminates the caller process; never launch parallel probes. Capture, but do not modify:

```powershell
wsl.exe --version
wsl.exe --status
wsl.exe -l -v
cmd.exe /c ver
Get-Content "$env:USERPROFILE\.wslconfig" -ErrorAction SilentlyContinue
Get-WinEvent -LogName "Microsoft-Windows-LxssManager/Operational" -MaxEvents 100
```

Inside Ubuntu, only after it is responsive:

```bash
cat /proc/version
cat /etc/os-release
cat /etc/wsl.conf 2>/dev/null
ps -p 1 -o comm=
systemctl is-system-running
mount
df -h
free -h
ps -ef
```

If the same hang reproduces, capture a `VmmemWSL` memory dump before another restart where operationally feasible. Do not infer the failing layer solely from a lingering `wsl.exe` process.

### Stop conditions

- Any WSL command still hangs: stop migration and collect diagnostics.
- Any unexplained CIFS/SMB, network, VPN, security-software, or systemd mount issue: resolve and re-test first.
- Any missing or unverified backup: no Docker changes.

## Phase 1 - backup and rehearsal

1. Freeze writes to stateful containers.
2. Record container inspect output, image digests, volume/network inventories, bind mounts, ports, secrets references, and Compose configuration.
3. Create application-consistent database backups; a live filesystem copy is not sufficient.
4. Export images and named-volume data using portable formats.
5. Preserve Docker Desktop’s VHDX and export Ubuntu to backup storage outside the WSL virtual disk where feasible.
6. Verify backup readability and perform an isolated restore/rehearsal before changing the production-like Ubuntu instance.

No raw secrets, credentials, or PII may enter the repository, plan, logs, or backup manifests.

## Phase 2 - prepare native Engine

1. Confirm Ubuntu version is supported by Docker’s current Ubuntu instructions.
2. Enable systemd in `/etc/wsl.conf`, then restart WSL only during the approved maintenance window.
3. Install Docker Engine, CLI, containerd, Buildx, and Compose from Docker’s signed Ubuntu repository.
4. Start rootful Docker through systemd; do not run rootful and rootless daemons against one socket.
5. Keep Docker storage in Ubuntu’s Linux filesystem (`/var/lib/docker` and, on newer installations, possibly `/var/lib/containerd`). Do not copy Docker Desktop’s internal layer/database directories into the native Engine store.
6. Verify Unix-socket-only access. Do not configure `tcp://0.0.0.0:2375`.
7. Add the user to the `docker` group only with explicit acceptance that this grants root-equivalent host control.

## Phase 3 - recreate and validate workloads

Recreate networks, volumes, and containers from Compose or documented definitions. Validate:

- intended image digests;
- volume and bind-mount paths, ownership, and permissions;
- service discovery and published ports;
- health checks, restart policies, logs, and startup ordering;
- persistent reads, writes, restarts, and database-native integrity checks;
- authentication, background jobs, scheduled tasks, and integrations;
- `docker info` endpoint and root directory show native Ubuntu Engine;
- WSL restart and Windows reboot persistence.

Require zero unexplained differences between pre-cutover and post-cutover manifests.

## Phase 4 - Desktop removal

Only after Phase 3 passes and rollback is proven:

1. Switch CLI configuration away from `desktop-linux`; remove stale `DOCKER_HOST`, `DOCKER_CONTEXT`, TLS, and Desktop credential-helper dependencies from the intended environments.
2. Retain Docker Desktop backups and the original VHDX for the agreed rollback period.
3. Stop Docker Desktop.
4. Uninstall Docker Desktop through Windows Apps.
5. Do not delete residual Docker data or unregister any WSL distro until retention expiry and explicit confirmation.

## Rollback triggers

Abort and restore the prior path if backup restore fails, inventory/checksums diverge, database integrity fails, writes fail, required secrets are missing, networking breaks, performance is unacceptable, or logs contain unexplained daemon/storage errors.

## Acceptance criteria

- Ubuntu starts and executes bounded probes reliably across repeated sessions.
- Native Engine passes `docker info`, Compose, Buildx, and `hello-world` checks.
- Every in-scope workload passes read/write/restart and application smoke tests.
- Persistent data reconciles with the source backup and no writes diverged across copies.
- WSL restart and Windows reboot restore the intended Docker services.
- Docker API remains local Unix-socket-only.
- Docker Desktop rollback data is independently readable and retained.
- A human owner confirms application acceptance before Desktop removal.

## Required human review

Before Phase 4: confirm backup location/retention, stateful workload ownership, rootful security acceptance, and the maintenance window. This plan does not authorize destructive cleanup by itself.
