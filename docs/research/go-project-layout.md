# Go project layout for a daemon and CLI

## Question

Should a Go application with a long-running daemon and an interactive CLI ship two executables, such as `foobar` and `foobard`, or one executable with a daemon subcommand, such as `foobar daemon`? How should the repository be arranged?

## Finding

Go does not prescribe either product shape. Both appear in mature projects. The deployment boundary should decide the executable boundary.

Use one executable when the daemon and CLI form one installed product, run under the same trust policy, and nearly always ship together. Use two executables when client-only or server-only installations are common, the daemon has distinct privileges or dependencies, or users connect to a central service from other machines.

Go's official module layout guide says that repositories containing multiple programs typically give each program its own directory. `cmd/` is a common convention rather than a toolchain rule. A top-level `internal/` directory can hold private packages shared by those programs. The Go toolchain enforces the import boundary for `internal/`.

Sources:

- [Go module layout: multiple commands](https://go.dev/doc/modules/layout#multiple-commands)
- [Go module layout: server projects](https://go.dev/doc/modules/layout#server-project)
- [Go command documentation: internal directories](https://pkg.go.dev/cmd/go#hdr-Internal_Directories)
- [Go specification: program execution](https://go.dev/ref/spec#Program_execution)
- [Go blog: package names](https://go.dev/blog/package-names)

## Ecosystem examples

### Separate daemon and CLI executables

#### Docker: `docker` and `dockerd`

Docker separates the user client from the daemon. The client sends API requests over a Unix socket or network connection; the daemon owns containers, images, networks, and volumes.

The source is also split across repositories:

- [`docker/cli/cmd/docker`](https://github.com/docker/cli/tree/master/cmd/docker) contains the CLI entry point.
- [`moby/moby/cmd/dockerd`](https://github.com/moby/moby/tree/master/cmd/dockerd) contains the daemon entry point.
- [`moby/moby/client`](https://github.com/moby/moby/tree/master/client) contains the Engine client.
- [`moby/moby/api`](https://github.com/moby/moby/tree/master/api) contains API types.

Docker packages the engine and CLI separately on Linux, and its system service starts only `dockerd`. This fits deployments where clients may connect to a remote daemon and where the daemon has a distinct operational role.

Sources:

- [Docker architecture overview](https://docs.docker.com/get-started/docker-overview/)
- [Docker Engine installation packages](https://docs.docker.com/engine/install/ubuntu/)
- [Docker systemd service](https://github.com/moby/moby/blob/master/contrib/init/systemd/docker.service)

#### Tailscale: `tailscale` and `tailscaled`

Tailscale ships an interactive client and a persistent network daemon from one repository:

- [`cmd/tailscale`](https://github.com/tailscale/tailscale/tree/main/cmd/tailscale) contains the CLI.
- [`cmd/tailscaled`](https://github.com/tailscale/tailscale/tree/main/cmd/tailscaled) contains the daemon.
- [`client/local`](https://github.com/tailscale/tailscale/tree/main/client/local) contains the LocalAPI client used to talk to the daemon.

The daemon owns network state, routing, DNS integration, and a local control socket. A Debian package installs the CLI in `/usr/bin`, the daemon in `/usr/sbin`, and a service unit for the daemon. The separate executables match their different privilege and lifecycle needs.

Sources:

- [Tailscale repository README](https://github.com/tailscale/tailscale/blob/main/README.md)
- [Tailscale systemd service](https://github.com/tailscale/tailscale/blob/main/cmd/tailscaled/tailscaled.service)
- [Tailscale Unix package definition](https://github.com/tailscale/tailscale/blob/main/release/dist/unixpkgs/pkgs.go)

#### etcd: `etcdctl` and `etcd`

etcd separates its network server from its administrative client:

- [`server`](https://github.com/etcd-io/etcd/tree/main/server) builds `etcd`.
- [`etcdctl`](https://github.com/etcd-io/etcd/tree/main/etcdctl) builds `etcdctl`.
- [`client/v3`](https://github.com/etcd-io/etcd/tree/main/client/v3) contains the reusable gRPC client.
- [`api`](https://github.com/etcd-io/etcd/tree/main/api) contains protocol definitions.

`etcdctl` may run on a different host from the server. The project therefore gives the client, protocol, and server clear package and executable boundaries.

#### Prometheus: `prometheus` and `promtool`

Prometheus builds two primary executables from [`cmd/prometheus`](https://github.com/prometheus/prometheus/tree/main/cmd/prometheus) and [`cmd/promtool`](https://github.com/prometheus/prometheus/tree/main/cmd/promtool). `promtool` performs local validation and some remote operations while sharing configuration, rule, query, and model packages with the server.

This example shows how two programs in one module can share domain packages without importing each other's `main` packages. It is a weaker daemon-control example because `promtool` is also an offline validation tool.

### One executable with a long-running subcommand

#### Caddy: `caddy run`

Caddy's executable entry point is [`cmd/caddy/main.go`](https://github.com/caddyserver/caddy/blob/master/cmd/caddy/main.go). Its command registry includes foreground `run`, detached `start`, `reload`, `stop`, configuration tools, and diagnostic commands. The reusable server runtime lives outside the command adapter.

A service manager can invoke `caddy run` directly. The executable name does not need a `d` suffix for systemd or another supervisor to manage it.

#### Syncthing: `syncthing serve`

Syncthing's [`cmd/syncthing`](https://github.com/syncthing/syncthing/tree/main/cmd/syncthing) command tree contains the default long-running `serve` command as well as remote CLI operations, browser commands, configuration generation, and diagnostics. Its service runtime lives under [`lib/syncthing`](https://github.com/syncthing/syncthing/tree/main/lib/syncthing).

#### rclone: `rclone serve`

rclone ships one executable containing file operations and several servers. [`cmd/serve`](https://github.com/rclone/rclone/tree/master/cmd/serve) contains subcommands for HTTP, FTP, SFTP, WebDAV, S3, NFS, and other protocols. Shared storage and HTTP machinery lives outside those command adapters.

#### Consul: `consul agent`

Consul's [`command`](https://github.com/hashicorp/consul/tree/main/command) tree includes the long-running `agent` role and user operations such as catalog, configuration, key/value, membership, and operator commands. The daemon runtime lives in [`agent`](https://github.com/hashicorp/consul/tree/main/agent).

#### Vault: `vault server` and `vault agent`

Vault's [`command`](https://github.com/hashicorp/vault/tree/main/command) tree includes `server`, `agent`, and interactive operations such as read, write, authentication, and operator commands. The core runtime and HTTP behavior live in separate packages.

Consul and Vault now use the Business Source License 1.1. They remain useful architectural examples but are no longer OSI-licensed open-source examples.

## What the examples show

Both shapes are established:

| Condition | Better fit |
| --- | --- |
| One product, one install, one release | One executable with a long-running subcommand |
| Client-only and server-only installs | Separate executables |
| Daemon requires capabilities or special executable policy | Separate executables |
| Client and daemon release independently | Separate executables |
| One artifact and command namespace matter most | One executable |
| Several independent clients use a central service | Separate executables |
| Daemon dependencies should stay out of the client | Separate executables |

A second executable supports an operational or security boundary but does not create one. The service account, socket or network permissions, authentication, authorization, filesystem permissions, and service sandbox enforce that boundary.

One executable also does not remove version skew. Replacing the file on disk does not restart an old daemon process, so a newly invoked CLI may still contact an older running daemon. Either design may need a protocol-version handshake.

## Recommendation for this project

This project will have one central daemon that owns a set of files and runs a small web server. Users will install a CLI that talks to the central daemon to access, upload, delete, and edit those files.

Ship two executables from one Go module:

```text
foobar       # user CLI
foobard      # central file service and web server
```

This system has client-only installations, one server deployment, a network protocol, and distinct server dependencies. Those are concrete executable boundaries. The CLI should contain no direct path to the managed files, even when invoked on the daemon host.

If product naming makes a `d` suffix awkward, `foobar-server` is also clear. The important choice is the executable boundary, not the suffix.

## Recommended repository layout

```text
foobar/
├── go.mod
├── cmd/
│   ├── foobar/
│   │   └── main.go
│   └── foobard/
│       └── main.go
├── internal/
│   ├── cli/             # commands, terminal output, editor workflow
│   ├── client/          # typed HTTP client
│   ├── daemon/          # startup, shutdown, signals, configuration
│   ├── httpserver/      # routes, handlers, auth, wire mapping
│   ├── filestore/       # path checks and atomic file operations
│   └── registry/        # domain rules and types
├── web/                 # embedded web UI
├── integration/
└── deploy/
    └── foobard.service
```

The directory names below `cmd/` determine the default executable names when building with:

```sh
go build ./cmd/...
```

Keep both `main` packages small. They should assemble dependencies, invoke their application adapter, and map failures to exit codes. They should not contain file, HTTP, or domain rules.

```go
// cmd/foobar/main.go
func main() {
    os.Exit(cli.Run(os.Args[1:]))
}
```

```go
// cmd/foobard/main.go
func main() {
    os.Exit(daemon.Run(os.Args[1:]))
}
```

Neither `main` package should import the other. Both may import focused packages under `internal/`.

## Ownership and protocol boundary

The daemon should be the sole owner of the controlled files:

```text
foobar CLI ──HTTP API──> foobard ──> filestore
                         │
                         └──> embedded web UI
```

The daemon should enforce:

- authentication and authorization
- path normalization and traversal prevention
- atomic replacement of file contents
- concurrent-edit checks
- file size and type limits
- audit records where the product needs them

Keep HTTP request and response structs at the HTTP seam. Map those wire types to small domain types before calling the registry or file store. The CLI's typed client should perform the reverse mapping. This prevents JSON and HTTP details from spreading through the application.

If third parties will consume the HTTP API, store an OpenAPI document in a top-level API or specification directory and treat it as a public contract. Keep the Go server and client adapters separate from that document. If only the bundled CLI and web UI use the API, an internal protocol package is enough.

## Editing workflow

Editing should use optimistic concurrency:

1. The CLI downloads the file and its current revision or ETag.
2. The CLI writes the content to a temporary file and opens `$EDITOR`.
3. The CLI uploads the changed content with the original revision.
4. The daemon rejects the write if the stored revision changed in the meantime.

An HTTP exchange can use `ETag` and `If-Match`:

```http
GET /files/example
ETag: "revision-123"

PUT /files/example
If-Match: "revision-123"
```

The daemon can return `412 Precondition Failed` when the revision no longer matches. The CLI should preserve the user's temporary file and explain how to retry or merge the changes.

Uploads and deletes should use the same revision checks when overwriting or removing existing files. The file store should write new content to a temporary file in the target filesystem and rename it atomically after validation.

## Deployment and packaging

Run the daemon in the foreground under a service manager. Do not fork or double-fork inside the daemon.

```ini
[Service]
ExecStart=/usr/bin/foobard --config /etc/foobar/config.toml
User=foobar
Restart=on-failure
```

The project can publish both executables in one release while allowing separate packages:

```text
foobar-client    # installs foobar
foobar-server    # installs foobard, configuration, and service unit
```

Use the same release version initially. Add an explicit protocol version or capability exchange before independent client and server releases become necessary. If the service listens beyond a trusted host, require TLS and authenticated requests rather than relying on network location.
