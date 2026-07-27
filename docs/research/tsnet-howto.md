# How to run a daemon as an embedded Tailscale node with `tsnet`

Use [`tailscale.com/tsnet`](https://pkg.go.dev/tailscale.com/tsnet) when a Go daemon should join a tailnet under its own identity and expose or reach services through that tailnet. The daemon does not need a local `tailscaled` process, a TUN device, or root access.

This guide covers a durable, unattended daemon first. It then describes interactive login, OAuth, workload identity, ephemeral nodes, outbound connections, HTTPS, and the failure modes that matter in production.

## What `tsnet` adds to the process

A `tsnet.Server` runs a Tailscale node and a userspace TCP/IP stack inside the Go process. The embedded node:

- creates and stores its own machine and node keys;
- registers with the Tailscale coordination server;
- receives Tailscale IP addresses, a network map, DNS settings, routes, and access policy;
- encrypts peer traffic with WireGuard;
- connects directly to peers when possible and uses DERP relays when direct paths fail; and
- enforces tailnet policy before traffic reaches the daemon.

The host's Tailscale installation, if it has one, remains a separate node. The daemon gets its own machine entry, identity, addresses, name, and policy grants.

`tsnet` keeps application traffic in its userspace stack. `Server.Listen` exposes a listener through the tailnet without opening the same port on the host's normal network interfaces. `Server.Dial` makes outbound connections through the embedded node.

Tailscale's coordination service distributes keys and network state. It does not carry application data. Peer data takes a direct encrypted path or an encrypted path through DERP. See [Control and data planes](https://tailscale.com/docs/concepts/control-data-planes) and [Node keys](https://tailscale.com/docs/concepts/node-keys).

## Choose the node lifetime

Choose the state and enrollment model before writing the startup code.

### Durable daemon

Use a durable node for a service that should keep the same Tailscale identity and IP across restarts:

- set an explicit, private `Server.Dir`;
- keep that directory on persistent storage;
- use a one-off tagged auth key for first enrollment, or mint one with OAuth or workload identity;
- leave `Server.Ephemeral` false; and
- retain the state after removing the first-use credential.

This is the usual model for a systemd service, VM daemon, or stateful container.

### Disposable daemon

Use an ephemeral node for jobs, autoscaled replicas, and disposable containers:

- set `Server.Ephemeral` to true;
- use an ephemeral reusable auth key, OAuth, or workload identity;
- give every live replica its own state; and
- expect a new node identity and Tailscale IP when a fully stateless replica starts again.

`Ephemeral: true` changes registration behavior, but it does not select an in-memory state store. With the default store, tsnet still writes enrollment state beneath `Server.Dir`. If every process start must create a fresh node, use an in-memory state store:

```go
import (
	"tailscale.com/ipn/store/mem"
	"tailscale.com/types/logger"
)

store, err := mem.New(logger.Discard, "")
if err != nil {
	return fmt.Errorf("create in-memory tsnet store: %w", err)
}

srv := &tsnet.Server{
	Hostname:  "orders-job",
	Dir:       "/run/orders-job/tsnet",
	Ephemeral: true,
	Store:     store,
}
```

This makes enrollment state memory-backed. tsnet still uses `Dir` for logging configuration and buffers, so the directory must exist or have a writable parent. Ephemeral machines normally disappear from the Machines page 30–60 minutes after they go offline. See [Ephemeral nodes](https://tailscale.com/docs/features/ephemeral-nodes).

Do not copy one state directory into several live replicas. Every copy would claim the same node identity and Tailscale IP.

## Prepare the tailnet

### 1. Define a service tag

Give the daemon a tag rather than tying it to a person's identity. Add a tag owner and grants to the tailnet policy. This standalone example lets one group reach the daemon on port 8080; replace the sample user and merge the entries into the tailnet's existing policy:

```jsonc
{
  "groups": {
    "group:platform": ["operator@example.com"]
  },

  "tagOwners": {
    "tag:orders-daemon": ["group:platform"]
  },

  "grants": [
    {
      "src": ["group:platform"],
      "dst": ["tag:orders-daemon"],
      "ip": ["tcp:8080"]
    }
  ]
}
```

If the daemon also needs outbound access, define the destination identity and add a separate grant for the required protocol and port.

A requested tag does not grant traffic by itself. Tag ownership controls who may apply the identity; grants or ACLs control what that identity may reach. See [Tags](https://tailscale.com/docs/features/tags).

### 2. Create an enrollment credential

For the simplest durable installation, create an auth key in the Tailscale admin console with these properties:

- **Reusable:** off;
- **Ephemeral:** off;
- **Pre-approved:** on if the tailnet uses device approval;
- **Tags:** `tag:orders-daemon`.

A one-off key limits the damage if the bootstrap secret leaks. It can enroll one node. Once enrollment succeeds, the saved node state lets the daemon restart without the key.

If the service must recreate nodes without an operator, use one of these instead:

- a narrowly tagged reusable key;
- an OAuth client that can create auth keys for the service tag; or
- workload identity federation on a supported runtime.

Auth-key expiration and revocation stop later enrollments. They do not remove machines already enrolled with that key. Delete or deauthorize an enrolled machine separately. See [Auth keys](https://tailscale.com/docs/features/access-control/auth-keys) and [Secure auth keys](https://tailscale.com/docs/features/access-control/auth-keys/how-to/secure-auth-keys).

If Tailnet Lock is active, sign the enrolled node or use a suitable pre-signed auth key. See [Tailnet Lock](https://tailscale.com/docs/features/tailnet-lock).

## Add `tsnet` to the daemon

Add the module and pin the selected version in `go.mod`. This guide was checked against v1.102.0:

```console
go get tailscale.com@v1.102.0
go mod tidy
```

Review the selected module's Go version requirement before changing a daemon with an older toolchain. Upgrade the pinned version deliberately rather than resolving `@latest` during each build.

Create one `tsnet.Server` for each Tailscale identity. Set all exported fields before the first call to `Start`, `Up`, `Listen`, `ListenTLS`, `Dial`, or `LocalClient`. Startup is one-shot; a closed server cannot restart.

The following example joins the tailnet, waits for the embedded node to become usable, and serves HTTP only inside the tailnet:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	stateDir := os.Getenv("TSNET_DIR")
	if stateDir == "" {
		return errors.New("TSNET_DIR is required")
	}

	authKey, err := readAuthKey()
	if err != nil {
		return err
	}

	srv := &tsnet.Server{
		Hostname:      "orders-daemon",
		Dir:           stateDir,
		AuthKey:       authKey,
		AdvertiseTags: []string{"tag:orders-daemon"},
		UserLogf:      log.Printf,
	}

	if err := srv.Start(); err != nil {
		return fmt.Errorf("start tsnet node: %w", err)
	}
	defer srv.Close()

	startupCtx, cancelStartup := context.WithTimeout(ctx, 2*time.Minute)
	_, err = srv.Up(startupCtx)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("join tailnet: %w", err)
	}

	ln, err := srv.Listen("tcp", ":8080")
	if err != nil {
		return fmt.Errorf("listen on tailnet: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()

		shutdownCtx, cancelShutdown := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancelShutdown()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("serving on orders-daemon:8080")
	if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func readAuthKey() (string, error) {
	path := os.Getenv("TS_AUTHKEY_FILE")
	if path == "" {
		return os.Getenv("TS_AUTHKEY"), nil
	}

	key, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Tailscale auth key: %w", err)
	}
	return strings.TrimSpace(string(key)), nil
}
```

`TSNET_DIR` and `TS_AUTHKEY_FILE` are configuration names defined by this example daemon. tsnet does not read them itself.

`Start` initializes the embedded node. Defer `Close` only after `Start` succeeds; current tsnet versions can panic if `Close` follows an early initialization failure. `Up` then waits for the `Running` state, which makes it a useful readiness gate. `Listen` also calls `Start` implicitly: it can return and the application can enter its accept loop before the node reaches `Running`. An unapproved node still cannot exchange tailnet traffic.

The startup timeout should fit the deployment system's readiness budget. A pending interactive login, device approval, or Tailnet Lock signature can keep `Up` waiting.

## Launch and enroll the daemon

### First launch with an auth key

For a local test, create a private state directory and read the key without placing it in shell history:

```console
export TSNET_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/orders-daemon/tsnet"
install -d -m 0700 "$TSNET_DIR"
read -rsp 'Tailscale auth key: ' TS_AUTHKEY && printf '\n'
export TS_AUTHKEY
./orders-daemon
unset TS_AUTHKEY
```

The `tskey-auth-…` value comes from the Tailscale admin console; do not type that literal placeholder. If the production daemon will run under systemd, enroll it through the unit in the next section so the service owns its state from the first launch.

Current tsnet releases also read `TS_AUTHKEY` themselves. Assigning it to `Server.AuthKey` makes the daemon's input explicit and works with the field's documented API.

On successful enrollment, the node appears in the Machines page as `orders-daemon`, or with a numeric suffix if that name already exists. Confirm that it has:

- the expected `tag:orders-daemon` identity;
- device approval, if required;
- a Tailnet Lock signature, if required; and
- the expected Tailscale IP addresses.

Find the assigned MagicDNS name in the Machines page or LocalAPI status. A name collision may add a numeric suffix. From an allowed peer, use that assigned name to check the health endpoint:

```console
curl -i http://<assigned-machine-name>:8080/healthz
```

A successful check returns HTTP 204.

`Server.Listen("tcp", ":8080")` does not expose `0.0.0.0:8080` or `[::]:8080` on the host. Only traffic accepted by the embedded Tailscale node can reach it.

### Later launches

With the default file store, tsnet writes enrollment state to:

```text
<Server.Dir>/tailscaled.state
```

For the example, that is:

```text
/var/lib/orders-daemon/tsnet/tailscaled.state
```

Once this state records a valid enrollment, tsnet reconnects with the saved identity. A direct auth key supplied later does not replace that identity, so a consumed one-off key is enough for the first launch.

OAuth and workload-identity credentials need more care: current tsnet resolves them before it loads saved enrollment. Leaving either method configured can mint and discard an auth key on every restart, and an expired or revoked credential can block startup despite valid saved state. Remove `TS_CLIENT_SECRET`, `TS_CLIENT_ID`, `TS_ID_TOKEN`, and `TS_AUDIENCE` after durable enrollment unless the process must be able to enroll fresh state.

After the first enrollment:

1. stop the daemon;
2. remove the one-off auth key from its environment or secret file;
3. leave the state directory in place; and
4. start the daemon again and confirm that it returns under the same machine entry and IP.

Use a dedicated service account and restrict `Server.Dir` to that account. Current tsnet creates its directory with mode `0700` when its parent is writable. Set the permissions in the service manager or image as well.

Treat `tailscaled.state` as a private key:

- keep it out of source control and general backups;
- encrypt any backup that includes it;
- restore one copy to one node only; and
- remove the machine from the tailnet and enroll fresh state if the file leaks.

The default directory derives from `os.UserConfigDir()` and the binary name. That default can change when a service account, home directory, container image, or binary name changes. Production daemons should always set `Dir`.

## Run under systemd

A systemd unit can create and protect the state directory:

```ini
[Unit]
Description=Orders daemon over Tailscale
Wants=network-online.target
After=network-online.target

[Service]
DynamicUser=yes
StateDirectory=orders-daemon
StateDirectoryMode=0700
Environment=TSNET_DIR=/var/lib/orders-daemon/tsnet
LoadCredential=ts-authkey:/etc/orders-daemon.authkey
Environment=TS_AUTHKEY_FILE=%d/ts-authkey
ExecStart=/usr/local/bin/orders-daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

With `StateDirectory=orders-daemon`, systemd creates `/var/lib/orders-daemon` and makes it writable by the dynamic user. The code above puts tsnet state in `/var/lib/orders-daemon/tsnet`.

For first enrollment, write only the auth key to `/etc/orders-daemon.authkey` and set mode `0600`. `LoadCredential` copies it into a protected credential directory for the service; `%d` expands to that directory. Use `LoadCredentialEncrypted` if the host manages encrypted systemd credentials.

Start the service and inspect its logs:

```console
systemctl enable --now orders-daemon
journalctl -u orders-daemon
```

After the machine joins, restart it once and confirm that it returns under the same machine entry. Then remove the `LoadCredential` and `TS_AUTHKEY_FILE` lines from the unit, delete `/etc/orders-daemon.authkey`, run `systemctl daemon-reload`, and restart the service again. The saved state now supplies the identity.

Environment variables are a poor place for long-lived secrets under systemd. The direct `TS_AUTHKEY` path remains useful for orchestrators whose secret manager injects an environment variable, while `TS_AUTHKEY_FILE` lets this unit use systemd credentials.

## Use another authentication method

Saved node state determines the node identity after credential resolution. Direct auth keys do not replace an existing identity. OAuth and workload-identity inputs are resolved first, so remove them after durable enrollment to avoid needless key minting and startup failures. Remove or log out the existing identity when you intend to enroll a different node. Set `TSNET_FORCE_LOGIN=1` only for a deliberate re-enrollment, then remove it; leaving it set makes every restart attempt a fresh login.

When no saved enrollment exists, current tsnet chooses credentials in this order:

1. `Server.AuthKey`;
2. `TS_AUTHKEY`;
3. legacy `TS_AUTH_KEY`;
4. `Server.ClientSecret` or `TS_CLIENT_SECRET`;
5. workload identity configured with a client ID and token or audience;
6. interactive browser login.

A stale higher-priority variable can mask the method you meant to use.

### Interactive browser login

Leave all bootstrap credentials unset. Keep `UserLogf` enabled. tsnet prints a login URL while the node needs authentication:

```console
./orders-daemon
```

Open the printed URL in a browser and authenticate with a user allowed to add the node and apply its requested tag. Approve or sign the machine if the tailnet requires either step.

Interactive login suits manual setup. It makes unattended restart recovery depend on a person if the state directory is lost or the node key expires.

`RunWebClient` does not solve first enrollment. It exposes the device management UI on port 5252 of an already reachable tsnet node. If you enable it, grant port 5252 only to operators who need it and configure the web UI capability described in [Device web interface](https://tailscale.com/docs/features/client/device-web-interface).

### OAuth client

Use OAuth when the runtime can hold an OAuth secret and must recreate nodes without a pre-generated reusable auth key.

Create an OAuth client with:

- `auth_keys` write scope; and
- permission to create every tag in `Server.AdvertiseTags`.

Set at least one advertised tag. OAuth enrollment requires a tagged service identity.

Current tsnet accepts the OAuth client secret through `Server.ClientSecret` or `TS_CLIENT_SECRET`. OAuth enrollment defaults to an ephemeral, unapproved node. Its client-secret options control whether the minted auth key is ephemeral and pre-approved. For a durable, pre-approved node, the current form is:

```dotenv
TS_CLIENT_SECRET=tskey-client-…?ephemeral=false&preauthorized=true
```

Quote the value in shells and deployment configuration that interpret `?` or `&`.

OAuth mints a one-off auth key during enrollment. The saved node state handles later restarts after you remove the client secret. If the client secret remains set, tsnet resolves it and can mint a key before loading the saved identity; an invalid secret can therefore block a restart.

See [OAuth clients](https://tailscale.com/docs/features/oauth-clients) and the selected module version's [`tsnet.Server` documentation](https://pkg.go.dev/tailscale.com/tsnet#Server).

### Workload identity federation

Use workload identity where the runtime can issue an OIDC identity, such as supported AWS, Google Cloud, or GitHub Actions environments. This removes the long-lived Tailscale bootstrap secret.

Add the feature registration import to the binary:

```go
import _ "tailscale.com/feature/identityfederation"
```

Without this import, tsnet ignores the workload-identity fields and falls through to another login method.

Configure a Tailscale federated identity with:

- `auth_keys` scope;
- the expected issuer, audience, subject, and claims;
- permission for every requested service tag; and
- the same tags supplied in `Server.AdvertiseTags`.

Then supply `TS_CLIENT_ID` with exactly one of:

- `TS_ID_TOKEN`, when the deployment obtains the provider's OIDC token itself; or
- `TS_AUDIENCE`, when tsnet supports automatic token discovery for the runtime.

With no query suffix, current workload-identity enrollment defaults to an ephemeral, unapproved node. The client-ID options control ephemeral registration and pre-approval. Once a query suffix is present, omitted Boolean options take their false value, so set both options explicitly. For a durable, pre-approved node, that form is:

```dotenv
TS_CLIENT_ID=client-id?ephemeral=false&preauthorized=true
TS_AUDIENCE=api.tailscale.com/client-id
```

For an ephemeral pre-approved node, set both booleans explicitly:

```dotenv
TS_CLIENT_ID=client-id?ephemeral=true&preauthorized=true
TS_AUDIENCE=api.tailscale.com/client-id
```

Use the actual client ID in the audience value. Check the selected tsnet version and provider instructions before deployment. See [Workload identity federation](https://tailscale.com/docs/features/workload-identity-federation).

## Make outbound tailnet connections

Use `Server.Dial` when the daemon should connect to another tailnet service through its tsnet identity:

```go
conn, err := srv.Dial(ctx, "tcp", "orders-database:5432")
if err != nil {
	return fmt.Errorf("connect to orders database: %w", err)
}
defer conn.Close()
```

The destination can be a MagicDNS name or Tailscale IP. The tailnet policy sees the connection as coming from the embedded node's user or tag identity.

For libraries that accept a dial function, wrap `srv.Dial` or build an `http.Transport` with a matching `DialContext`. Avoid routing unrelated process traffic through tsnet by accident; inject the tsnet dialer only into clients that need the tailnet.

## Serve HTTPS with a tailnet certificate

`Server.ListenTLS` obtains and renews a certificate through the embedded LocalAPI:

```go
ln, err := srv.ListenTLS("tcp", ":443")
if err != nil {
	return fmt.Errorf("listen with tailnet TLS: %w", err)
}
```

This requires MagicDNS and HTTPS certificates to be enabled for the tailnet. Pass the exact network string `"tcp"`; `ListenTLS` rejects `"tcp4"` and `"tcp6"`. Connect with the certificate's full MagicDNS name, such as `https://orders-daemon.example-tailnet.ts.net`, because a certificate does not validate for the bare `https://orders-daemon` name.

Tailscale HTTPS certificate names enter public Certificate Transparency logs, so choose a machine name that does not reveal confidential information. See [Enabling HTTPS](https://tailscale.com/docs/how-to/set-up-https-certificates).

## Inspect and manage the embedded node

`Server.LocalClient()` returns the LocalAPI client for the embedded node. It can inspect status and perform operations such as interactive login and logout. The method returns both a client and an error:

```go
lc, err := srv.LocalClient()
if err != nil {
	return fmt.Errorf("open tsnet LocalAPI: %w", err)
}
if err := lc.Logout(ctx); err != nil {
	return fmt.Errorf("log out tsnet node: %w", err)
}
```

`Server.Close` shuts down listeners, connections, the userspace stack, and the control connection. Enrollment survives when the configured state store persists it, including the default `Dir/tailscaled.state` store. An in-memory store loses enrollment when the process exits.

Logout has a different meaning. `Client.Logout` invalidates the current login and requires authentication next time. Logging out an ephemeral node removes it immediately. Call logout only when the operator intends to discard that enrollment.

Tagged devices have node-key expiry disabled by default, though a tailnet administrator can enable it. If a node key expires, the daemon cannot pass traffic until it authenticates again. See [Key expiry](https://tailscale.com/docs/features/access-control/key-expiry).

## Troubleshoot startup and connectivity

### `Up` waits until its context expires

Check these conditions in order:

1. The daemon can reach the Tailscale control plane.
2. `UserLogf` shows an interactive login URL or a credential error.
3. The auth key has not expired or already been consumed.
4. The OAuth client or federated identity has `auth_keys` scope and owns the requested tags.
5. The Machines page is waiting for device approval.
6. Tailnet Lock is waiting for a node signature.

An approval or signature wait can look like a network timeout from the process.

### The machine appears, but peers cannot connect

Confirm that:

- the machine carries the intended tag;
- the machine is approved and signed where required;
- policy grants the source identity access to the tag and port;
- the daemon listens through `srv.Listen`, rather than only through the host's `net.Listen`; and
- DNS resolves the expected machine name.

A machine-name collision may add a numeric suffix. Use the Machines page or LocalAPI status to find the assigned name.

### A new auth key is ignored

Existing `tailscaled.state` takes precedence. Use the current enrollment, call LocalAPI logout, or deliberately set `TSNET_FORCE_LOGIN=1` for one launch. Removing the state file without removing the old machine entry can leave a stale machine in the tailnet.

### Restarts create duplicate machines

The state directory is missing, read-only, mounted at a different path, or cleared between starts. Set `Server.Dir` to durable storage and remove `TSNET_FORCE_LOGIN=1`.

A consumed one-off key cannot recover a lost state volume. Create another key, or use OAuth or workload identity to mint a fresh one.

### Replicas interfere with each other

They share a copied `tailscaled.state`. Give each persistent replica its own state volume. Use ephemeral registration and separate or in-memory state for disposable replicas.

### OAuth or workload identity falls back to browser login

Check credential precedence first. Remove stale `TS_AUTHKEY`, `TS_AUTH_KEY`, or `TS_CLIENT_SECRET` values that mask the intended method.

For OAuth, set `AdvertiseTags` and grant the OAuth client access to those tags.

For workload identity, also check the blank feature import, client ID, issuer, audience, subject, claims, and the rule that exactly one token source is set. The federated identity page records detailed claim-matching failures that token-exchange responses may omit.

## Production checklist

Before shipping the daemon:

- Pin the `tailscale.com` module version and test its Go toolchain requirement.
- Use a service tag and grant only the peers, protocols, and ports the daemon needs.
- Give each durable node a unique, private, persistent `Server.Dir`.
- Keep enrollment credentials and `tailscaled.state` out of source control.
- Prefer a tagged one-off key for a single durable node.
- Prefer workload identity for disposable nodes on supported runtimes.
- Make enrollment pre-approved when device approval is enabled, or plan the approval step.
- Plan Tailnet Lock signing before unattended deployment.
- Call `Up` with a bounded context before reporting readiness.
- Keep `UserLogf` during enrollment so operators can see login and status messages.
- Shut down the application listener, then call `Server.Close`.
- Use logout only when discarding the node identity.
- Monitor node-key expiry and keep the Tailscale module updated.
- Restrict the optional device web UI if `RunWebClient` is enabled.

## References

- [tsnet feature overview](https://tailscale.com/docs/features/tsnet)
- [`tsnet` package API](https://pkg.go.dev/tailscale.com/tsnet)
- [`tsnet.Server` package API](https://pkg.go.dev/tailscale.com/tsnet#Server)
- [`tsnet` source](https://github.com/tailscale/tailscale/blob/main/tsnet/tsnet.go)
- [Auth keys](https://tailscale.com/docs/features/access-control/auth-keys)
- [Secure auth keys](https://tailscale.com/docs/features/access-control/auth-keys/how-to/secure-auth-keys)
- [OAuth clients](https://tailscale.com/docs/features/oauth-clients)
- [Workload identity federation](https://tailscale.com/docs/features/workload-identity-federation)
- [Ephemeral nodes](https://tailscale.com/docs/features/ephemeral-nodes)
- [Node keys](https://tailscale.com/docs/concepts/node-keys)
- [Key expiry](https://tailscale.com/docs/features/access-control/key-expiry)
- [Control and data planes](https://tailscale.com/docs/concepts/control-data-planes)
- [Tags](https://tailscale.com/docs/features/tags)
- [Device approval](https://tailscale.com/docs/features/access-control/device-management/device-approval)
- [Tailnet Lock](https://tailscale.com/docs/features/tailnet-lock)
- [Device web interface](https://tailscale.com/docs/features/client/device-web-interface)
- [Tailscale HTTPS certificates](https://tailscale.com/docs/how-to/set-up-https-certificates)
