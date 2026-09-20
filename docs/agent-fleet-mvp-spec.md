# agent-fleet MVP Specification

**Status:** Proposed / implementation-ready after owner review  
**Version:** v0.1 + Agent scope amendment (2026-09-20)
**Date:** 2026-09-10  
**Primary goal:** Centrally manage Agent CLI versions, model/provider configuration, MCP configuration, Skills, rules, and drift across multiple developer machines through a Web UI, with `agentd` as the continuous management channel and SSH as a first-class bootstrap / fallback / SSH-only management channel.

---

## 1. Executive Summary

`agent-fleet` is a single-operator Agent workstation fleet manager for Linux, macOS, and WSL machines.

It treats the state of developer Agents as desired state instead of as a collection of manually edited dotfiles and one-off installation commands. A machine can be continuously managed by `agentd`, managed on demand over SSH, or use both. The control plane exposes a Web UI and API for machine inventory, profiles, Skills, MCP, model settings, deployments, drift, reconcile, and rollback.

The MVP deliberately does **not** try to become an Agent task scheduler, remote shell product, secret manager, general-purpose configuration management system, or Kubernetes replacement. It manages the **Agent development environment**, not Agent workloads.

The MVP support target includes eight Agent families through adapters, following the owner’s 2026-09-20 screenshot supplement:

- Claude (exact runtime identity to be verified; do not assume Claude Code)
- Codex
- DeepSeek Harness
- Grok
- Hermes
- Oh-My-Pi (OMP / oh-my-pi; one family, not two)
- ZCode
- OpenCode (retained from the original scope)

These are required support targets, not claims of implemented or verified compatibility. See [Agent support scope and acceptance](agent-support-matrix.md) for identity, capability, and per-family validation requirements. Screenshot labels such as built-in, public, online, and run count do not define Fleet capabilities.

The architecture must make adding Gemini CLI or other future Agents possible without changing control-plane core logic.

---

## 2. Problem Statement

A developer using multiple machines currently has to repeat the same maintenance work on every machine:

- install or upgrade Agent CLIs;
- configure model/provider endpoints;
- update MCP servers;
- synchronize Skills;
- maintain Agent-specific rules and configuration files;
- discover version/config drift;
- recover a machine after an Agent or configuration upgrade breaks;
- maintain SSH entries for remote development machines.

The same conceptual configuration is represented differently by each Agent. For example, Codex, OMP, and OpenCode may use different configuration paths, formats, Skill directories, and MCP formats.

The required system must provide one control plane that maps a normalized desired state onto those Agent-specific representations and continuously reports whether every machine matches the desired state.

---

## 3. Design Principles

### 3.1 Desired state over imperative actions

The primary operation is not "upgrade Codex on machine A". The primary operation is "machine A should match profile X at generation N".

Imperative actions such as `Reconcile`, `Repair agentd`, or `Rollback` exist, but they operate relative to stored desired state.

### 3.2 SSH is a first-class transport

SSH is not merely an emergency fallback. It is used for:

1. machine discovery and initial probe;
2. `agentd` bootstrap;
3. `agentd` repair or replacement;
4. management of machines where a persistent daemon is not allowed;
5. one-shot reconcile using the same adapter/reconciler code as daemon mode;
6. generating/exporting OpenSSH host inventory compatible with remote-development workflows such as Codex Remote SSH.

### 3.3 `agentd` is an outbound continuous-management channel

`agentd` initiates a persistent mTLS connection to the control plane. Nodes do not need an inbound management port.

`agentd` owns local discovery, adapters, diff calculation, transactional apply, backup, health checks, and observed-state collection.

### 3.4 SSH and daemon mode reuse the same reconciler

There must not be two implementations of Agent configuration logic.

The `agentd` binary has two execution modes:

```text
agentd daemon
agentd oneshot inventory | plan | apply
```

For an SSH-only machine, the server executes the remote `agentd oneshot` command through SSH and streams a desired-state manifest to it.

### 3.5 Manage only owned fields

Agent configuration files may contain settings unknown to `agent-fleet`. The MVP must preserve unmanaged fields.

Adapters define which fields they own. Drift is computed over the managed projection, not over the entire file.

### 3.6 Secrets stay out of the MVP control plane

The MVP manages **references to environment variable names**, not secret values.

Example:

```yaml
provider:
  endpoint: https://example.internal/v1
  apiKeyEnv: OPENAI_API_KEY
```

`agent-fleet` must not read, upload, synchronize, or persist the value of `OPENAI_API_KEY`.

### 3.7 Optimize for a single trusted operator first

MVP targets an individual developer or small trusted lab environment. Multi-tenant authorization, SSO, enterprise RBAC, audit export, and organization policy are later features.

---

## 4. MVP Scope

### 4.1 In scope

- Web dashboard.
- Single control-plane instance.
- SQLite persistence.
- Linux amd64/arm64.
- macOS amd64/arm64.
- WSL treated as Linux.
- SSH host registration and probing.
- SSH config import by host alias.
- SSH bootstrap of `agentd`.
- Persistent outbound `agentd` connection over mTLS.
- SSH-only machine mode using `agentd oneshot`.
- Inventory for OS, architecture, hostname, Agent versions, Skills, and managed configuration hashes.
- Agent adapters for all eight families listed in §1, with per-family acceptance defined in `agent-support-matrix.md`.
- Desired Agent version management.
- Normalized model/provider configuration without secret values.
- MCP configuration management.
- Skills sourced from Git repositories or local directories on the control plane.
- Rules / Agent instruction file management.
- Desired/observed state.
- Drift detection.
- Manual and automatic reconcile.
- Backup before mutation.
- Rollback to the immediately previous successful managed state.
- Canary/batch deployments across multiple machines.
- Agentd repair through SSH.
- Export of an OpenSSH include file containing Fleet-managed host aliases.
- REST API plus server-sent events for UI live updates.

### 4.2 Explicitly out of scope

- Running user Agent tasks from the Fleet control plane.
- Proxying Codex/OMP/OpenCode interactive sessions.
- Embedded Web SSH terminal.
- Full SSH key lifecycle / SSH CA.
- Secret synchronization.
- Password-based SSH authentication.
- Multi-user / multi-tenant RBAC.
- Kubernetes CRDs or Kubernetes as a required runtime.
- Windows native nodes outside WSL.
- Skill marketplace / public registry discovery.
- Arbitrary machine configuration beyond Agent-related state.
- Central log collection for Agent conversations.
- Agent conversation/history synchronization.
- Automatic modification of the user's primary `~/.ssh/config` without explicit opt-in.

---

## 5. Target User Experience

### 5.1 Add a machine

From Web UI:

```text
Machines -> Add Machine

SSH Host Alias: gpu-home
Management Mode: agentd + ssh
Profile: default-dev
```

The server:

1. resolves the SSH host with local OpenSSH configuration;
2. tests non-interactive SSH connectivity;
3. probes OS, architecture, home directory, shell, service manager, and current Agents;
4. displays the result;
5. when the operator clicks `Bootstrap agentd`, installs the matching `agentd` binary;
6. installs a user service where supported;
7. completes one-time enrollment;
8. waits for the outbound mTLS stream;
9. marks the node `AgentConnected=True`.

### 5.2 Apply a profile

The operator assigns profile `default-dev` to three machines.

The profile contains:

- desired Codex version;
- desired OpenCode version;
- OMP model endpoint/model name;
- three Skills;
- MCP definitions;
- managed Agent rules.

The control plane calculates an effective desired state and increments a generation.

Online `agentd` nodes receive the new generation immediately. SSH-only nodes are reconciled through a one-shot SSH operation when an operator starts a reconcile or a Deployment targets them. The MVP does not continuously poll SSH-only nodes in the background.

### 5.3 Detect drift

If a user manually edits a managed Codex setting or changes a Skill target, `agentd` reports a different managed-state digest.

The Web UI shows:

```text
wsl-gpu
Drifted: True

codex.config.model
  desired: gpt-5.6
  observed: other-model

skill/superpowers
  desired: 91c7f3...
  observed: 3a82d1...
```

The operator can inspect the diff and click `Reconcile`.

### 5.4 Recover from broken `agentd`

If the stream disappears but SSH still works:

```text
AgentConnected: False
SSHReachable: True
```

The Web UI exposes `Repair agentd`.

The server uses SSH to run diagnostics, replace/restart the binary/service if necessary, and waits for reconnection.

---

## 6. High-Level Architecture

```text
                         Browser
                            |
                            v
                 +---------------------+
                 | agent-fleet-server  |
                 |---------------------|
                 | Web UI              |
                 | REST API            |
                 | SSE Events          |
                 | Reconcile Ctrl      |
                 | Deployment Ctrl     |
                 | SSH Ctrl            |
                 | Skill Resolver      |
                 | SQLite Store        |
                 +----+-----------+----+
                      |           |
                SSH   |           | mTLS bidirectional gRPC
                      |           |
        +-------------+--+     +--+--------------+
        | Machine A      |     | Machine B       |
        |----------------|     |-----------------|
        | sshd           |     | sshd            |
        | agentd daemon  |     | agentd daemon   |
        | adapters       |     | adapters        |
        | Codex / OMP    |     | OpenCode / OMP  |
        +----------------+     +-----------------+

        +----------------+
        | Machine C      |
        | SSH-only       |
        |----------------|
        | sshd           |
        | agentd oneshot |
        | adapters       |
        +----------------+
```

### 6.1 Control plane components

`agent-fleet-server` is a single process in MVP.

Logical modules:

- HTTP/API server
- SSE event hub
- Machine controller
- Reconcile controller
- Deployment controller
- SSH controller
- Enrollment/certificate service
- Skill resolver/cache
- Desired-state renderer
- SQLite repository

These are package/module boundaries, not separate services.

### 6.2 Node-side components

One binary: `agent-fleet-agentd`.

Subcommands:

```text
agent-fleet-agentd daemon
agent-fleet-agentd oneshot inventory
agent-fleet-agentd oneshot plan
agent-fleet-agentd oneshot apply
agent-fleet-agentd doctor
agent-fleet-agentd version
```

The daemon and one-shot commands must use the same adapter and local reconciler packages.

---

## 7. Deployment Model

### 7.1 Default development deployment

The control plane runs on an operator-controlled machine.

Defaults:

```text
Web/API:       127.0.0.1:7788
Agent gRPC:    0.0.0.0:7789
Data dir:      ~/.local/share/agent-fleet/
Config dir:    ~/.config/agent-fleet/
```

macOS paths may follow XDG-compatible paths for MVP; native Library/Application Support migration is not required.

### 7.2 Agent endpoint reachability

Persistent `agentd` mode requires nodes to reach the configured control-plane `advertiseURL`.

Example:

```yaml
server:
  agentListen: 0.0.0.0:7789
  advertiseURL: https://fleet-host.example:7789
```

If a node cannot reach the control plane, it remains valid as an SSH-only node.

The MVP does not create reverse SSH tunnels automatically.

### 7.3 Agentd release artifacts

The server must have access to server-compatible `agentd` release artifacts for each supported OS/architecture. The expected layout is:

```text
<data-dir>/artifacts/agentd/<version>/<goos>/<goarch>/agent-fleet-agentd
```

Bootstrap and repair select the artifact by the SSH probe result. The server must be able to replace an incompatible/stale `agentd` through SSH. Automatic daemon self-update is not required in MVP.

---

## 8. Domain Model

The API is JSON, but resources follow a K8s-like `metadata/spec/status` structure.

### 8.1 Common metadata

```json
{
  "id": "uuid",
  "name": "wsl-gpu",
  "createdAt": "...",
  "updatedAt": "...",
  "generation": 7
}
```

Names are unique per resource type.

### 8.2 Machine

```yaml
kind: Machine
metadata:
  name: wsl-gpu
spec:
  managementMode: agentd   # agentd | ssh
  ssh:
    hostAlias: wsl-gpu
    # Alternatively, machines created directly by Fleet may store explicit
    # HostName/User/Port/ProxyJump/IdentityFile paths for export.
  profileRef: default-dev
  overrides: {}
status:
  observedGeneration: 7
  os: linux
  arch: amd64
  hostname: dev-wsl
  homeDir: /home/user
  agentdVersion: 0.1.0
  desiredDigest: sha256:...
  observedDigest: sha256:...
  lastHeartbeatAt: ...
  lastInventoryAt: ...
  conditions:
    - type: SSHReachable
      status: "True"
    - type: AgentConnected
      status: "True"
    - type: Drifted
      status: "False"
    - type: Reconciled
      status: "True"
```

`managementMode=agentd` means the preferred reconcile path is the daemon. SSH is still retained for bootstrap/repair if configured.

`managementMode=ssh` means reconcile is always initiated through SSH using `agentd oneshot`.

### 8.3 AgentProfile

```yaml
kind: AgentProfile
metadata:
  name: default-dev
spec:
  agents:
    codex:
      enabled: true
      version: "0.153.0"
      installer:
        driver: command
        argv:
          - npm
          - install
          - -g
          - "@openai/codex@${VERSION}"
      config:
        model: gpt-5.6
        providerRef: openai-main

    omp:
      enabled: true
      version: "0.42.0"
      installer:
        driver: command
        argv: ["/usr/local/bin/install-omp", "${VERSION}"]
      config:
        model: qwen-local
        providerRef: local-vllm

    opencode:
      enabled: true
      version: "1.18.30"
      installer:
        driver: command
        argv: ["/usr/local/bin/install-opencode", "${VERSION}"]

  skills:
    - skillRef: superpowers
    - skillRef: adversarial-review

  mcp:
    local-tools:
      command: /usr/local/bin/my-mcp-server
      args: ["--stdio"]
      envRefs:
        SERVICE_TOKEN: SERVICE_TOKEN

  rules:
    global:
      content: |
        Follow repository instructions and preserve tests.
```

Installer commands are trusted-operator configuration. They must be executed without a shell by default; `${VERSION}` substitution is performed per argument.

A future installer-driver layer may add `npm`, `brew`, `mise`, and signed binary releases. MVP only requires `command` plus adapter-provided defaults where safe and verified.

### 8.4 ModelProvider

```yaml
kind: ModelProvider
metadata:
  name: openai-main
spec:
  type: openai-compatible
  endpoint: https://api.openai.com/v1
  apiKeyEnv: OPENAI_API_KEY
```

No credential value is stored.

### 8.5 Skill

```yaml
kind: Skill
metadata:
  name: superpowers
spec:
  source:
    type: git
    repo: https://github.com/example/superpowers.git
    ref: v4.3.0
    path: skills/superpowers
status:
  resolvedRevision: 91c7f314...
  contentDigest: sha256:...
  resolvedAt: ...
```

Local source is also allowed:

```yaml
source:
  type: local
  path: /path/on/control-plane/skills/my-skill
```

A Skill is considered valid if the resolved directory exists and contains `SKILL.md`.

### 8.6 Deployment

```yaml
kind: Deployment
metadata:
  name: codex-0-153-rollout
spec:
  selector:
    machineNames: [macbook, wsl-gpu, devbox-01]
  targetGeneration: 12
  strategy:
    canary: 1
    batchSize: 1
    maxUnavailable: 1
    pauseOnFailure: true
status:
  phase: RollingOut
  succeeded: 1
  failed: 0
  pending: 2
```

### 8.7 Operation

Every mutation creates an immutable operation record.

```yaml
kind: Operation
metadata:
  id: uuid
spec:
  machine: wsl-gpu
  type: Reconcile
  transport: agentd
  desiredGeneration: 12
status:
  phase: Succeeded
  startedAt: ...
  finishedAt: ...
  steps:
    - name: backup
      phase: Succeeded
    - name: codex-version
      phase: Succeeded
    - name: codex-config
      phase: Succeeded
    - name: skills
      phase: Succeeded
```

Operation output must redact environment values and must never capture secrets.

---

## 9. Effective Desired State

The server resolves a machine into one immutable `DesiredStateSnapshot`.

Inputs:

```text
AgentProfile
+ referenced Skills resolved to exact revisions/digests
+ referenced ModelProviders
+ Machine overrides
+ adapter schema version
```

The output includes only normalized desired state and immutable Skill artifacts.

Example:

```yaml
machine: wsl-gpu
generation: 12
agents:
  codex:
    version: 0.153.0
    config:
      model: gpt-5.6
      provider:
        endpoint: https://api.openai.com/v1
        apiKeyEnv: OPENAI_API_KEY
skills:
  superpowers:
    revision: 91c7f314...
    digest: sha256:...
    artifactDigest: sha256:4bc9...
```

The snapshot itself receives a deterministic SHA-256 digest.

Generation changes only when effective desired state changes.

---

## 10. Observed State and Drift

### 10.1 Observed state

`agentd` collects:

- installed Agent versions;
- adapter-managed config projection;
- Skill target revisions/content digests;
- MCP managed projection;
- rules managed projection;
- adapter health;
- service health.

### 10.2 Managed projection

Adapters must not hash entire config files if only some fields are owned.

Example:

```json
{
  "codex": {
    "version": "0.153.0",
    "managedConfig": {
      "model": "gpt-5.6",
      "model_provider": "openai-main"
    }
  }
}
```

The observed digest is calculated over normalized managed state.

### 10.3 Drift semantics

A machine is `Drifted=True` when:

```text
observed managed state != effective desired managed state
```

Unmanaged local configuration must not trigger drift.

### 10.4 Reporting intervals

Defaults:

- heartbeat: every 15 seconds;
- offline threshold: 45 seconds;
- full inventory: at connect, after every operation, and every 5 minutes;
- drift report: immediately after local inventory detects a changed managed digest.

Intervals must be configurable.

---

## 11. `agentd` Protocol

Use protobuf + bidirectional gRPC over TLS.

### 11.1 Connection direction

```text
agentd -> control plane
```

The node initiates the connection.

### 11.2 One logical stream

```proto
service FleetAgentService {
  rpc Connect(stream AgentToServer) returns (stream ServerToAgent);
  rpc FetchArtifact(ArtifactRequest) returns (stream ArtifactChunk);
}

service FleetEnrollmentService {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);
  rpc RenewCertificate(RenewCertificateRequest) returns (RenewCertificateResponse);
}
```

### 11.3 Agent-to-server messages

Union payloads:

- `Hello`
- `Heartbeat`
- `ObservedState`
- `OperationStarted`
- `OperationProgress`
- `OperationResult`
- `LogEvent`

`LogEvent` is for operational logs only and must not include Agent conversation content.

### 11.4 Server-to-agent messages

Union payloads:

- `Welcome`
- `DesiredStateChanged`
- `ExecuteOperation`
- `CancelOperation`
- `RequestInventory`

### 11.5 Reconnection

The daemon reconnects with exponential backoff with jitter.

A reconnect always starts with `Hello` followed by a full observed-state snapshot.

The server must be idempotent to duplicate operation result delivery.

---

## 12. Enrollment and mTLS

### 12.1 Bootstrap flow

1. Server creates Machine record.
2. SSH probe identifies platform.
3. Server creates a single-use enrollment token with a short expiry.
4. Server copies the matching `agentd` binary and bootstrap config over SSH.
5. `agentd` generates a local private key.
6. `agentd` connects to an enrollment endpoint with Machine ID + token + CSR.
7. Server verifies token and signs a client certificate with the Fleet CA.
8. Token becomes invalid.
9. `agentd` stores the private key/certificate with user-only permissions.
10. `agentd` opens the normal mTLS `Connect` stream.

### 12.2 Certificate lifecycle

MVP:

- Fleet server creates a local CA on first start.
- Agent certificates have a finite lifetime.
- Agentd renews before expiry over the authenticated mTLS channel.
- CA private key remains on the control-plane host and uses file permissions `0600`.

Enterprise-grade external PKI/HSM support is not MVP.

---

## 13. SSH Transport

### 13.1 Use the system OpenSSH client

MVP should **not** reimplement OpenSSH semantics with a Go SSH library.

The server invokes the local OpenSSH client through a constrained executor. This preserves existing behavior for:

- `Host` aliases;
- `Include`;
- `ProxyJump`;
- identity selection;
- `known_hosts`;
- SSH agent integration;
- organization-specific SSH configuration.

Typical commands:

```text
ssh -G <alias>
ssh -o BatchMode=yes <alias> -- <remote command>
scp ...
```

No shell is used for local command construction. Remote command arguments must be safely quoted through a dedicated utility.

### 13.2 Authentication

MVP supports whatever non-interactive authentication the operator's local OpenSSH client already supports.

Password prompts are unsupported.

The server does not ingest SSH private-key content into SQLite.

### 13.3 Known-host policy

Default: preserve standard OpenSSH host-key checking.

The Fleet UI must surface host-key failures; it must not silently set `StrictHostKeyChecking=no`.

### 13.4 SSH-only reconcile

SSH-only machines may exist specifically because they cannot reach the control-plane Agent endpoint. Therefore Skill artifacts must not depend on mTLS artifact download in this mode.

The server builds an immutable operation bundle containing:

```text
manifest.json                 # DesiredStateSnapshot + digests
artifacts/skills/<digest>/... # only referenced Skill artifacts
```

The bundle itself has a SHA-256 digest. The server uploads it to a remote temporary directory and invokes the same local reconciler:

```text
server
  -> build operation bundle
  -> scp bundle to machine
  -> ssh machine 'agent-fleet-agentd oneshot plan --bundle <remote-path>'
  <- plan
  -> ssh machine 'agent-fleet-agentd oneshot apply --bundle <remote-path>'
  <- OperationResult
  -> remove remote temporary bundle
```

`agentd` verifies all referenced artifact digests before planning/applying. If `agentd` is not installed, the server uploads a temporary compatible binary first.

For daemon-managed nodes, immutable Skill artifacts are fetched through authenticated `FetchArtifact` gRPC streaming and verified locally.

### 13.5 Exporting SSH inventory

Fleet can render:

```text
~/.ssh/agent-fleet.conf
```

Example:

```sshconfig
Host gpu-home
  HostName 192.168.1.100
  User user

Host devbox-01
  HostName devbox.internal
  User dev
  ProxyJump bastion
```

The MVP provides:

- preview;
- download/export;
- an explicit local action to install/update the generated include file.

Machines that were imported only by an existing `hostAlias` already exist in the operator's OpenSSH configuration and do not need to be duplicated into the generated file. Fleet exports hosts for which it owns explicit SSH connection fields.

It does **not** silently rewrite the operator's primary SSH config.

Compatibility rationale: Codex Desktop Remote SSH can discover hosts from OpenSSH configuration, so a Fleet-maintained include file creates a clean integration point without proxying Codex sessions.

---

## 14. Local Reconciler

The local reconciler is shared by daemon and one-shot modes.

### 14.1 Reconcile phases

```text
1. validate desired state
2. inventory current state
3. calculate plan
4. backup managed files / Skill links / version metadata
5. apply Agent versions
6. apply normalized Agent config
7. materialize Skills
8. apply MCP
9. apply rules
10. run adapter health checks
11. inventory again
12. verify desired == observed
13. commit operation success
```

### 14.2 Failure behavior

If a mutable step fails:

- stop subsequent steps;
- preserve error diagnostics;
- attempt automatic restore from the operation backup;
- inventory after restore;
- report both original failure and restore result.

If restore itself fails, mark the machine `Degraded=True` and do not continue automatic rollouts to that machine.

### 14.3 Idempotency

Applying the same desired snapshot twice must produce no material change on the second execution.

---

## 15. Agent Adapter Contract

The adapter boundary is a core MVP requirement.

Go interface conceptually:

```go
type Adapter interface {
    ID() string
    Detect(ctx context.Context) (DetectedAgent, error)
    Inventory(ctx context.Context, desired AgentDesiredState) (AgentObservedState, error)
    Plan(ctx context.Context, desired AgentDesiredState, observed AgentObservedState) ([]Change, error)
    Apply(ctx context.Context, desired AgentDesiredState, changes []Change) error
    HealthCheck(ctx context.Context, desired AgentDesiredState) error
}
```

Additional interfaces may be split by concern, but control-plane code must never directly know Agent-specific config paths/formats.

Each adapter defines:

- binary detection;
- version parsing;
- installer defaults;
- configuration paths;
- managed config schema;
- config merge strategy;
- Skill destinations;
- MCP representation;
- rules/instructions representation;
- health checks.

### 15.1 Codex adapter

MVP responsibilities:

- detect `codex` binary/version;
- manage selected fields in Codex configuration;
- manage Codex Skill targets;
- manage normalized MCP settings supported by the adapter;
- manage Fleet-owned Agent instructions/rules without erasing unmanaged content;
- validate by version probe plus configuration parse.

Exact paths and current schema must be encapsulated inside the adapter and covered by fixtures/tests. They must not leak into core packages.

### 15.2 Oh-My-Pi (OMP) adapter

Same contract. OMP-specific config and Skill locations remain local to the adapter.

Because OMP installation patterns may differ by environment, installer command must be overrideable in the profile.

### 15.3 OpenCode adapter

Same contract, including normalized MCP and Skills.

### 15.4 Additional required families and capability boundaries

Claude, DeepSeek Harness, Grok, Hermes, and ZCode must use the same adapter boundary. Resolve their exact executable/package/repository identity before choosing paths, installers, or configuration schemas. Do not substitute a similarly named model, API provider, or CLI.

Each family declares version/OS compatibility and support for configuration, model/provider settings, MCP, Skills, and rules. Unknown capability is unverified, not supported. An explicitly requested unsupported or unverified capability must fail validation before mutation with an actionable reason, never silently skip or report success. Both daemon and one-shot paths use the same declaration and adapter. Per-family fixtures and acceptance cases are specified in [the support matrix](agent-support-matrix.md).

---

## 16. Configuration Merge and Ownership

### 16.1 Structured files

For TOML / YAML / JSON:

- parse existing file;
- patch adapter-owned keys;
- preserve unknown keys;
- write atomically using temporary file + fsync + rename;
- preserve reasonable file permissions;
- verify parse after write.

### 16.2 Text rule files

Where partial ownership is possible, use explicit managed markers:

```text
# BEGIN agent-fleet managed
...
# END agent-fleet managed
```

If an Agent requires a file that cannot support safe partial ownership, the adapter must declare `ownership=whole-file`; the UI must make this visible.

### 16.3 Backups

Before every mutation, copy managed artifacts into:

```text
~/.local/share/agent-fleet/backups/<operation-id>/
```

Backups contain metadata sufficient for immediate rollback.

Retention default: last 10 successful/failed mutation operations per machine, configurable.

---

## 17. Skills

### 17.1 Resolution

The control plane resolves mutable source references into immutable revisions before deployment.

For Git:

```text
repo + ref + path
        -> fetch
        -> resolve exact commit
        -> verify SKILL.md
        -> compute content digest
        -> create immutable artifact
```

### 17.2 Artifact cache

Control plane cache:

```text
<data-dir>/artifacts/skills/<sha256>/...
```

Daemon-managed nodes download by digest through mTLS `FetchArtifact` streaming and verify SHA-256 before materialization. SSH-only nodes receive the same immutable artifacts inside the operation bundle described in section 13.4.

### 17.3 Node materialization

Canonical node cache:

```text
~/.local/share/agent-fleet/skills/<skill-name>/<digest>/
```

Adapters then symlink that immutable directory into each Agent's Skill destination when supported.

If symlinks are not supported for a target, copy mode may be used and the content digest becomes the drift signal.

### 17.4 Updates

A Skill update changes desired state only after the control plane resolves a new revision/digest and the operator assigns it (or explicitly chooses a future auto-update policy; auto-update is not MVP).

No `latest` floating deployment is permitted in the resolved desired snapshot.

---

## 18. Model and Provider Configuration

The normalized provider model is intentionally small.

Required fields:

```yaml
name: local-vllm
type: openai-compatible
endpoint: http://gpu-home:8000/v1
apiKeyEnv: OPTIONAL_ENV_NAME
```

Agent-specific adapters translate normalized values into native configuration.

The server and agentd do not inspect the referenced environment variable value.

The UI may run a provider connectivity check only if it can do so without accessing secret values. Authenticated model API validation is deferred.

---

## 19. MCP Management

Normalized MCP entry:

```yaml
mcp:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/work"]
    envRefs: {}
```

Rules:

- command and args are desired state;
- environment values are never stored;
- `envRefs` maps config keys to environment variable names only;
- adapters translate into Agent-native MCP configuration;
- adapter health check validates syntax/registration, not external service correctness.

---

## 20. Version Management

### 20.1 Desired version

Profiles use exact versions in the resolved snapshot.

No floating `latest` is allowed after resolution.

### 20.2 Installer abstraction

MVP provides:

```text
command installer
```

Configuration is argv-based and `${VERSION}` may appear inside individual arguments.

Requirements:

- execute directly, not through `sh -c`;
- capture exit status/stdout/stderr with size limits;
- redact known sensitive environment names;
- verify installed version using adapter detection after install;
- backup enough metadata to execute a configured rollback command or reinstall previous version.

Adapters may ship verified default installer commands, but every default must be overrideable.

### 20.3 Rollback

For Agent version changes, rollback means installing the previously observed version using the same installer mechanism.

If previous version installation is not possible, the deployment must report `RollbackUnsupported` instead of pretending rollback succeeded.

---

## 21. Deployment Controller

Fleet-wide changes are executed through Deployment resources.

### 21.1 Strategy

MVP supports:

- explicit machine list;
- canary count;
- integer batch size;
- `maxUnavailable`;
- `pauseOnFailure`.

### 21.2 Rollout algorithm

```text
Resolve target machines
-> filter unavailable/degraded machines
-> apply canary batch
-> require successful reconcile + health
-> continue batch by batch
-> pause immediately when failure policy triggers
```

No automatic time-based progressive rollout is required.

### 21.3 Health gate

A machine counts as successful only if:

- operation succeeded;
- post-apply inventory completed;
- observed managed digest equals desired digest;
- adapter health checks succeeded.

### 21.4 Rollback deployment

Rollback creates a new Deployment targeting a previous recorded desired generation. History is immutable.

---

## 22. Machine State and Conditions

Required conditions:

| Condition | Meaning |
|---|---|
| `SSHReachable` | Last SSH probe succeeded. |
| `AgentConnected` | Persistent daemon stream is active. |
| `InventoryReady` | At least one valid inventory has been received. |
| `Drifted` | Observed managed state differs from desired state. |
| `Reconciled` | Observed generation/digest matches desired. |
| `Degraded` | Reconcile or rollback left the machine in an unhealthy/uncertain state. |

Condition records include:

```text
status
reason
message
lastTransitionTime
```

Do not derive one condition only from another; store explicit controller decisions.

---

## 23. REST API

Prefix:

```text
/api/v1
```

### 23.1 Machines

```text
GET    /machines
POST   /machines
GET    /machines/{id}
PATCH  /machines/{id}
DELETE /machines/{id}

POST   /machines/{id}/ssh/probe
POST   /machines/{id}/bootstrap
POST   /machines/{id}/repair-agentd
POST   /machines/{id}/inventory
POST   /machines/{id}/reconcile
POST   /machines/{id}/rollback
GET    /machines/{id}/operations
GET    /machines/{id}/drift
```

### 23.2 Profiles

```text
GET    /profiles
POST   /profiles
GET    /profiles/{id}
PUT    /profiles/{id}
DELETE /profiles/{id}
GET    /profiles/{id}/render?machine=<id>
```

### 23.3 Skills

```text
GET    /skills
POST   /skills
GET    /skills/{id}
PUT    /skills/{id}
DELETE /skills/{id}
POST   /skills/{id}/resolve
GET    /skills/{id}/revisions
```

### 23.4 Providers

```text
GET    /providers
POST   /providers
GET    /providers/{id}
PUT    /providers/{id}
DELETE /providers/{id}
```

### 23.5 Deployments

```text
GET    /deployments
POST   /deployments
GET    /deployments/{id}
POST   /deployments/{id}/pause
POST   /deployments/{id}/resume
POST   /deployments/{id}/rollback
```

### 23.6 Events

```text
GET /events
Accept: text/event-stream
```

SSE events contain resource type/id plus a revision so the UI can selectively refetch data.

---

## 24. Web UI

Use a conventional SPA. React + TypeScript + Vite is recommended for MVP, but the API contract must not depend on the frontend framework.

Required pages:

### 24.1 Overview

Cards:

- total machines;
- daemon online;
- SSH reachable;
- drifted;
- degraded;
- active deployments.

Tables:

- machines requiring attention;
- recent deployments;
- recent failed operations.

### 24.2 Machines

Columns:

```text
Name | OS/Arch | Profile | agentd | SSH | Drift | Reconciled | Last Seen
```

Actions:

- Add Machine
- Probe SSH
- Bootstrap
- Reconcile
- Repair agentd

### 24.3 Machine Detail

Sections:

- connectivity/status;
- current Agents and versions;
- desired vs observed;
- Skills;
- model/provider;
- MCP;
- drift diff;
- operation history;
- bootstrap/repair actions.

Do not include an interactive shell in MVP.

### 24.4 Profiles

Editable normalized desired state with JSON/YAML preview.

Show which machines consume the profile.

### 24.5 Skills

Show:

```text
Name | Source | Requested Ref | Resolved Revision | Digest | Machines
```

Actions:

- Resolve
- View metadata
- Change ref

### 24.6 Deployments

Show canary/batch progress and per-machine result.

Actions:

- Pause
- Resume
- Rollback

### 24.7 SSH Inventory

Show Fleet machine aliases and rendered OpenSSH include-file preview.

Actions:

- Export
- Install/update managed include file only after explicit confirmation.

---

## 25. Persistence

Use SQLite with migrations.

Core tables conceptually:

```text
machines
profiles
providers
skills
skill_revisions
desired_snapshots
observed_states
deployments
deployment_targets
operations
operation_steps
events
agent_certificates
```

Large Skill artifact contents are stored on disk, not as SQLite blobs.

Observed-state history may retain only the latest state plus the state referenced by recent operations to limit unbounded growth.

---

## 26. Filesystem Layout

Control plane:

```text
~/.config/agent-fleet/
  config.yaml

~/.local/share/agent-fleet/
  fleet.db
  pki/
    ca.crt
    ca.key
    server.crt
    server.key
  artifacts/
    skills/<digest>/...
  git-cache/
  generated/
    ssh/agent-fleet.conf
```

Node:

```text
~/.config/agent-fleet/
  agentd.yaml

~/.local/share/agent-fleet/
  pki/
    client.crt
    client.key
    ca.crt
  skills/
  backups/
  state/
    last-observed.json
```

---

## 27. Recommended Repository Layout

```text
agent-fleet/
  README.md
  go.mod
  Makefile

  cmd/
    agent-fleet-server/
      main.go
    agent-fleet-agentd/
      main.go

  api/
    proto/
      fleet/v1/
        agent.proto
        state.proto
    openapi/

  internal/
    domain/
    store/
      sqlite/
    server/
      httpapi/
      sse/
      grpcagent/
    controller/
      machine/
      reconcile/
      deployment/
    sshtransport/
    enrollment/
    skills/
    desiredstate/
    operations/

    agentlocal/
      reconciler/
      inventory/
      backup/
      installer/
      adapter/
        codex/
        omp/
        opencode/

  web/
    src/
    package.json

  migrations/
  testdata/
    adapters/
    ssh/
  scripts/
  docs/
    architecture.md
    api.md
```

Keep node-local Agent-specific logic under `internal/agentlocal/adapter`. The control plane must not import Agent adapter implementation packages except for shared schema/validation where unavoidable.

---

## 28. Server Configuration

Example:

```yaml
server:
  httpListen: 127.0.0.1:7788
  agentListen: 0.0.0.0:7789
  advertiseURL: https://fleet.example:7789

ssh:
  configFiles:
    - ~/.ssh/config
  connectTimeout: 10s
  commandTimeout: 60s

agentd:
  heartbeatInterval: 15s
  offlineAfter: 45s
  inventoryInterval: 5m

storage:
  dataDir: ~/.local/share/agent-fleet

security:
  adminTokenEnv: AGENT_FLEET_ADMIN_TOKEN
```

If HTTP binds to a non-loopback address, an admin token is mandatory in MVP.

---

## 29. Security Requirements

MVP security requirements are non-optional.

1. Never persist model/provider API-key values.
2. Never persist SSH private-key contents in SQLite.
3. Use the local OpenSSH client and existing credential mechanisms.
4. Do not disable SSH host-key verification automatically.
5. Agentd transport uses mTLS.
6. Enrollment tokens are one-time and expiring.
7. CA/client private keys use restrictive filesystem permissions.
8. Mutation operations are recorded.
9. Command installer executes argv directly without implicit shell.
10. Logs cap output size and redact configured sensitive environment variable names.
11. Skill artifacts are digest-verified on nodes.
12. Config writes are atomic.
13. Every mutation takes a backup before changing managed state.
14. The Web API requires an admin token when exposed beyond loopback.

---

## 30. Error Handling

### 30.1 SSH errors

Expose distinct reasons:

```text
DNSResolveFailed
HostKeyVerificationFailed
AuthenticationFailed
ConnectionTimeout
RemoteCommandFailed
UnsupportedPlatform
```

Do not collapse all SSH errors into `unreachable`.

### 30.2 Agent errors

```text
AgentDisconnected
ProtocolVersionMismatch
EnrollmentRejected
CertificateExpired
InventoryFailed
```

### 30.3 Reconcile errors

```text
DesiredStateInvalid
InstallerFailed
VersionVerificationFailed
ConfigParseFailed
ConfigWriteFailed
SkillDownloadFailed
SkillDigestMismatch
HealthCheckFailed
RollbackFailed
```

Every user-visible error includes:

- reason code;
- human-readable message;
- operation ID;
- timestamp;
- safe diagnostics.

---

## 31. Compatibility and Schema Versioning

Both desired snapshots and gRPC protocol messages include:

```text
protocolVersion
schemaVersion
```

Rules:

- server rejects unsupported daemon major protocol versions;
- adapter desired-state schemas are versioned;
- agentd reports supported adapters/schema versions in `Hello`;
- server does not dispatch desired state an agentd cannot understand;
- UI surfaces upgrade requirement instead.

---

## 32. Testing Strategy

### 32.1 Unit tests

Required coverage:

- desired-state resolution;
- deterministic digest generation;
- managed projection and drift comparison;
- deployment batching/canary logic;
- version comparison;
- installer argv rendering;
- config merge preservation of unmanaged fields;
- Skill digest validation;
- adapter version parsing;
- condition transitions.

### 32.2 Adapter fixture tests

For each adapter, maintain fixture homes representing:

- Agent absent;
- old Agent version;
- desired version;
- valid unmanaged extra configuration;
- malformed configuration;
- existing Skills;
- manually drifted Skills;
- MCP config with unmanaged entries.

Tests must prove managed edits do not erase unmanaged settings.

### 32.3 gRPC integration tests

Use in-process transports to verify:

- enrollment;
- connection;
- desired-state notification;
- operation result;
- reconnect and duplicate result idempotency;
- protocol mismatch.

### 32.4 SSH integration tests

Use Linux containers running `sshd` to test:

- probe;
- bootstrap;
- one-shot inventory;
- one-shot reconcile;
- daemon repair flow;
- host-key failure;
- auth failure.

SSH tests should execute the real OpenSSH client.

### 32.5 Reconcile failure-injection tests

Inject failure after each mutation phase and verify:

- no later phase executes;
- rollback is attempted;
- final observed state is reported;
- `Degraded` is set only when state cannot be safely restored.

### 32.6 E2E MVP scenario

An automated Linux E2E must provision at least three SSH targets and demonstrate:

1. add machines;
2. bootstrap agentd on two;
3. keep one SSH-only;
4. apply one profile;
5. materialize a test Skill;
6. detect intentional drift;
7. reconcile drift;
8. run a canary deployment;
9. inject one failed target and observe rollout pause;
10. repair a stopped agentd through SSH.

macOS service installation is covered by unit/fixture tests plus a documented manual smoke test for MVP.

---

## 33. Observability

Server structured logs include:

```text
resource_type
resource_id
machine_id
operation_id
deployment_id
reason
```

Expose Prometheus-compatible metrics endpoint:

```text
/metrics
```

Minimum metrics:

```text
agent_fleet_machines_total
agent_fleet_agent_connected
agent_fleet_ssh_reachable
agent_fleet_machine_drifted
agent_fleet_operations_total{type,result}
agent_fleet_operation_duration_seconds{type}
agent_fleet_deployments_total{result}
agent_fleet_reconcile_total{result}
```

Do not include machine credentials, command output, or model secrets in metric labels.

---

## 34. MVP Acceptance Criteria

The MVP is accepted only when all of the following work in a reproducible demo.

### A. Fleet onboarding

- Add a Linux/WSL machine by existing SSH alias.
- Probe reports OS, architecture, home path, and installed supported Agents.
- Bootstrap installs `agentd` and the node becomes connected.

### B. SSH-only operation

- Add another machine with `managementMode=ssh`.
- Inventory and reconcile work through `agentd oneshot` without a daemon.

### C. Agent version management

- A profile pins a supported Agent to an exact version.
- Reconcile upgrades/downgrades through configured installer.
- Post-install version verification is mandatory.

### D. Configuration management

- Profile updates a managed model/provider field.
- Existing unmanaged fields remain unchanged.

### E. Skill management

- Git Skill source resolves to an exact commit/digest.
- Skill is materialized for at least two different Agent adapters.
- Manual modification or replacement produces drift.

### F. MCP management

- One normalized MCP entry is rendered into at least Codex and OpenCode native configuration through adapters.
- Existing unmanaged MCP entries are preserved where the native format allows it.

### G. Drift and reconcile

- Manual drift appears in the Web UI.
- `Show Diff` displays managed desired vs observed fields.
- `Reconcile` clears drift after successful apply.

### H. Deployment

- Three machines receive one desired-state change using a one-machine canary.
- Canary must become healthy before the next batch starts.
- An injected failure pauses rollout.

### I. Recovery

- Stop `agentd` on one daemon-managed machine.
- UI reports daemon offline while SSH remains reachable.
- `Repair agentd` restores the service and connection.

### J. Rollback

- A failed config or version change can restore the immediately previous successful managed state when the underlying installer supports the prior version.

### K. SSH inventory integration

- Fleet renders an OpenSSH include file for managed machines.
- The file can be exported without modifying the user's primary SSH config.

### L. Security

- No SSH private key or model API token value appears in SQLite, operation records, server logs, or observed-state payloads.

---

## 35. Non-Functional Requirements

### 35.1 Scale target

MVP target:

```text
<= 100 machines
<= 10 supported Agent instances per machine
<= 200 Skills in catalog
<= 20 concurrent mutation operations by default
```

This is not a hard architectural maximum; it is the validation target.

### 35.2 Reliability

- Server restart preserves all resource state.
- Agentd reconnect is automatic.
- In-flight operations become `Unknown` after server restart unless a later agent report proves their final result.
- Reconcile operations are idempotent.

### 35.3 Performance

For an online agentd node with no drift, a reconcile plan should normally complete in under one second excluding network/package-manager operations.

### 35.4 Upgradeability

SQLite schema changes use explicit migrations.

Protocol and desired-state schemas are versioned from the first MVP release.

---

## 36. Architectural Decisions

### AD-1: Single-process control plane for MVP

**Decision:** Web/API/controllers/SQLite/SSH orchestration remain in one server process.

**Reason:** The system needs strong logical boundaries, not distributed-system complexity. Splitting services provides little MVP value.

### AD-2: SQLite instead of PostgreSQL

**Decision:** SQLite is the only MVP database.

**Reason:** Single-operator deployment and <=100-node target do not justify external DB operation.

### AD-3: Outbound gRPC from agentd

**Decision:** agentd dials the control plane.

**Reason:** This avoids opening inbound node management ports and works naturally with laptops/devboxes behind normal firewalls when they can reach the Fleet endpoint.

### AD-4: System OpenSSH instead of a Go SSH stack

**Decision:** execute the operator's OpenSSH binary.

**Reason:** Reusing `~/.ssh/config`, ProxyJump, Include, known-host checking, ssh-agent, and enterprise SSH behavior is more important than having an all-Go transport implementation.

### AD-5: One reconciler for daemon and SSH-only modes

**Decision:** `agentd oneshot` runs the same local reconciler as `agentd daemon`.

**Reason:** This prevents behavior drift between transports and sharply reduces test surface.

### AD-6: No secret distribution in MVP

**Decision:** only environment-variable names are managed.

**Reason:** Secret lifecycle would materially increase security scope and distract from the core Fleet problem.

### AD-7: K8s-like resource semantics without Kubernetes dependency

**Decision:** use `metadata/spec/status`, generations, conditions, controllers, and desired/observed state internally; do not require CRDs.

**Reason:** This preserves a proven reconciliation model while keeping the product easy to run on a workstation.

### AD-8: No task execution proxy

**Decision:** Fleet does not proxy Codex/OMP/OpenCode interactive execution.

**Reason:** SSH/Codex Remote and native Agent runtimes already own execution. Fleet owns environment state.

---

## 37. Deferred Post-MVP Work

Not part of the first implementation, but the architecture should not block:

- native installer drivers: npm, brew, mise, binary release;
- native Windows node support;
- secret-manager integrations;
- SSO/RBAC and multi-user audit;
- PostgreSQL;
- GitOps repository as desired-state source;
- Skill registry/marketplace;
- automatic Skill update policies;
- SSH certificate authority integration;
- signed Agent/Skill artifacts;
- per-team/profile policy inheritance;
- approval gates for deployments;
- remote Fleet control plane with multiple operator clients;
- plugin SDK for third-party adapters;
- Webhook/event integrations;
- Prometheus/Grafana dashboards;
- OpenTelemetry traces;
- Kubernetes packaging/Helm chart;
- API compatibility for external automation.

---

## 38. Implementation Guardrails for Codex

When implementing this specification:

1. Do not collapse SSH and daemon execution into separate Agent-management implementations. Both must call the same local reconciler.
2. Do not put Agent-specific paths or configuration logic into controller/API packages. Keep them inside adapters.
3. Do not overwrite unknown user configuration fields unless an adapter explicitly declares whole-file ownership.
4. Do not implement secret storage as a convenience shortcut.
5. Do not execute installer strings via `sh -c`.
6. Do not disable SSH host-key verification.
7. Do not add Kubernetes, Redis, PostgreSQL, message queues, or a service mesh to the MVP.
8. Do not implement an embedded SSH terminal.
9. Do not make `latest` a resolved desired version/revision. Resolve to immutable versions/digests before apply.
10. Do not report reconcile success until post-apply inventory and adapter health checks prove desired == observed.
11. Prefer small packages with explicit interfaces; keep server controllers independent of filesystem/Agent-specific details.
12. Every mutation path must be testable with a temporary HOME and without touching the developer's real Agent configuration.

---

## 39. Definition of Done

`agent-fleet` MVP is done when a new developer can clone the repository, start the control plane, open the Web UI, register several SSH-accessible machines, bootstrap or one-shot-manage them, assign a normalized Agent profile, safely reconcile Codex/OMP/OpenCode environment state, detect deliberate drift, roll out a change with a canary, recover a failed `agentd` through SSH, and roll back the last managed state without leaking SSH keys or model credentials.

The system must demonstrate the complete control loop:

```text
Desired State
    -> resolve immutable snapshot
    -> dispatch through agentd or SSH
    -> local plan/apply
    -> health verification
    -> observed state
    -> drift decision
    -> Web UI status
```

That closed loop is the MVP product. Everything else is secondary.

---

## 40. Compatibility Note: Codex Remote SSH

The SSH inventory feature is intentionally designed to coexist with Codex Desktop Remote SSH rather than proxy it. OpenAI documented on 2026-05-14 that Codex Desktop Remote SSH can automatically detect hosts from the user's SSH configuration and create projects/run threads on remote machines. `agent-fleet` therefore treats generated OpenSSH host configuration as an integration surface while keeping Codex execution outside the Fleet control plane.

