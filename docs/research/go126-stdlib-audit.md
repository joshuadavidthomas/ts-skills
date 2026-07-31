# Go 1.26 Standard-Library Audit Notes

This note identifies current standard-library facilities worth checking before maintaining custom or third-party implementations in this Go web service. It is deliberately scoped to APIs that are relevant to this repository, not every Go 1.26 addition.

The module already declares `go 1.26.5`. The highest-value finding is the browser CSRF layer; the most useful repeatable audit mechanism is Go 1.26's rewritten `go fix`.

## Implementation status

The actionable findings in this note have been implemented. The browser portal now uses `http.CrossOriginProtection`; the Gorilla dependency and token, cookie, and persisted-key plumbing were removed. Managed client writer operations are confined by `os.Root` while retaining explicit no-symlink policy checks. Temporary transaction artifacts use root-relative exclusive creation with `crypto/rand.Text`, typed error extraction uses `errors.AsType`, and candidate identity generation no longer treats `crypto/rand.Read` failure as recoverable. The bounded ZIP preflight remains intentionally unchanged.

## Important correction: `CrossOriginProtection` arrived in Go 1.25

`net/http.CrossOriginProtection` is not new in Go 1.26. The Go 1.25 release notes introduced it, and the current package documentation marks the type as added in Go 1.25.0. It rejects non-safe cross-origin browser requests, using `Sec-Fetch-Site` when present and otherwise comparing the parsed `Origin` host with `Request.Host`. `GET`, `HEAD`, and `OPTIONS` are always allowed. Requests with neither header are allowed as presumed same-origin or non-browser traffic. [Go 1.25 release notes](https://go.dev/doc/go1.25#net/http), [`net/http.CrossOriginProtection` documentation](https://pkg.go.dev/net/http@go1.26.5#CrossOriginProtection), [implementation](https://go.dev/src/net/http/csrf.go)

The API surface is:

- `http.NewCrossOriginProtection()` to construct a protection value;
- `Handler` to wrap a handler and deny unsafe cross-origin requests;
- `Check` to apply the decision without invoking the deny handler;
- `SetDenyHandler` to customize denial;
- `AddTrustedOrigin` for explicitly trusted HTTPS origins; and
- `AddInsecureBypassPattern` for request patterns that must bypass protection.

The zero value is usable. The standard example wraps the entire server handler with `http.NewCrossOriginProtection().Handler(mux)`. Trusted origins are validated and stored in canonical `https://host[:port]` form; bypass patterns use `ServeMux` syntax and are explicitly described as insecure. [`net/http` example](https://go.dev/src/net/http/example_test.go), [`CrossOriginProtection` source](https://go.dev/src/net/http/csrf.go)

### Repository implication

At audit time, the browser application used `github.com/gorilla/csrf` in `internal/server/web/handler.go`, including a persisted 32-byte authentication key, a CSRF cookie, request tokens exposed to templates and the upload client, and special plaintext-development handling. The implementation now uses `http.CrossOriginProtection` and has removed that dependency and plumbing.

This must not be a mechanical middleware swap. The two mechanisms have different contracts:

- Gorilla's middleware is token-based. The stdlib facility is Fetch Metadata / Origin based and issues no token or cookie.
- The stdlib explicitly allows unsafe requests carrying neither `Sec-Fetch-Site` nor `Origin`; this preserves non-browser clients. It therefore does not establish browser intent for old or unusual clients that omit both headers.
- Safe methods are always allowed, so every `GET`, `HEAD`, and `OPTIONS` route must be verified side-effect free.
- Any trusted origins must be exact HTTPS origins. Avoid `AddInsecureBypassPattern` unless a route has a documented reason to opt out.

Audit heuristic: search for CSRF packages, CSRF token/cookie/key types, `Origin`, `Referer`, `Sec-Fetch-Site`, `crypto/subtle`, and unsafe-method checks. First determine the intended client/browser compatibility and threat model, then test the stdlib decision matrix before replacing token plumbing. In this repository, keep the machine API outside browser protection only if its tsnet peer identity and route separation are re-verified; wrapping the portal's route subtree is preferable to broad bypass patterns.

## Go 1.26 facilities relevant to this repository

### Run the new modernizers

Go 1.26 completely rewrote `go fix` as the home for modernizers. Its initial suite contains dozens of behavior-preserving migrations to newer language and core-library APIs and uses the same analysis framework as `go vet`. The official command for a module-wide pass is `go fix ./...`; because it writes source, run it on a clean branch and inspect/test the patch. [Go 1.26 release notes](https://go.dev/doc/go1.26#go-command), [official `go fix` guide](https://go.dev/blog/gofix)

Audit heuristic: add a periodic, review-only `go fix ./...` sweep when the module's Go version is raised. This is broader and less brittle than maintaining a private list of syntactic idioms. It will not discover architectural substitutions such as replacing a third-party CSRF strategy with `CrossOriginProtection`, so retain a release-note review alongside it.

For this audit, `go fix -diff ./...` completed with an empty diff. The current tree has no modernization edits recognized by the Go 1.26 fixer suite.

### Prefer `errors.AsType` for typed error extraction

Go 1.26 added `errors.AsType[E](err)`, a type-safe generic form of `errors.As` that is documented as faster and usually easier to use. This repository has several `errors.As` calls in production and test code. They are low-risk modernization candidates, especially declarations whose sole purpose is to pass `&target` to `errors.As`. [Go 1.26 release notes](https://go.dev/doc/go1.26#errors), [`errors.AsType` documentation](https://pkg.go.dev/errors@go1.26.5#AsType)

Audit heuristic: search for `errors.As(` and let `go fix` identify semantics-preserving conversions. Do not manually rewrite cases until pointer/value matching is checked; `AsType` follows `errors.As`'s assignability rules.

### Stop treating `crypto/rand.Read` as recoverable

Since Go 1.24, `crypto/rand.Read` always fills its argument and never returns an error; an underlying failure terminates the program irrecoverably. Go 1.26 further prevents callers of several cryptographic operations from substituting insecure randomness through their random-reader arguments. [`crypto/rand.Read` documentation](https://pkg.go.dev/crypto/rand@go1.26.5#Read), [Go 1.24 release notes](https://go.dev/doc/go1.24#crypto/rand), [Go 1.26 crypto changes](https://go.dev/doc/go1.26#crypto)

At audit time, the repository checked errors from `rand.Read` in candidate ID and temporary-name generation and used `io.ReadFull(rand.Reader, ...)` for the CSRF key. The CSRF key and custom temporary-name generator have since been removed, and candidate generation calls `rand.Read` without obsolete recovery handling.

For opaque textual secrets or identifiers whose exact encoding and length are not a protocol contract, `crypto/rand.Text()` (added in Go 1.24) returns RFC 4648 base32 text with at least 128 bits of randomness. Do not replace fixed-width candidate IDs, digests, or filesystem names mechanically where their lowercase-hex format is part of parsing, storage, tests, or UX. [`crypto/rand.Text` documentation](https://pkg.go.dev/crypto/rand@go1.26.5#Text)

Audit heuristic: search for `rand.Read`, `io.ReadFull(rand.Reader`, and random bytes followed by hex/base64 encoding. Classify each result as fixed binary/protocol data versus unconstrained text before choosing `rand.Read` or `rand.Text`.

## Relevant standard-library facilities from earlier Go releases

### Use `os.Root` to make project filesystem confinement race-resistant

Go 1.24 added `os.Root`, a filesystem handle whose methods constrain access beneath one directory even when path components are changed concurrently. Go 1.25 expanded it with operations including `MkdirAll`, `RemoveAll`, and `Rename`. On supported platforms the root is backed by a directory descriptor or handle and continues to reference the same directory across renames. [`os.Root` documentation](https://pkg.go.dev/os@go1.26.5#Root), [Go 1.24 release notes](https://go.dev/doc/go1.24#os), [Go 1.25 release notes](https://go.dev/doc/go1.25#os)

At audit time, the client tried to confine mutations by walking every component with `os.Lstat` and then using path-based operations. Writer transactions now open the project as an `os.Root` and perform managed renames, removals, reads, enumeration, syncing, and tree verification through that root.

`os.Root` is not a drop-in replacement for all current policy. It follows symlinks that remain inside the root, while this client deliberately rejects every symlink/reparse point in managed paths. Keep explicit `Lstat`-based policy checks where “no links” is a product invariant, but rely on root-relative operations for the security boundary. Also account for the platform caveats documented on `os.Root` rather than claiming identical guarantees on every target.

The staging-tree builders under `internal/tree` also join validated names to absolute staging paths before creating directories and files. They operate in freshly created private temporary directories and already enforce a portable cross-platform path policy, so an `os.Root` conversion there is lower priority. `filepath.IsLocal` or `filepath.Localize` cannot replace the portable-tree checks: they are host-platform lexical checks and do not enforce the repository's cross-platform case-folding, Windows-device-name, depth, collision, or size invariants. [`filepath.IsLocal` documentation](https://pkg.go.dev/path/filepath@go1.26.5#IsLocal), [`filepath.Localize` documentation](https://pkg.go.dev/path/filepath@go1.26.5#Localize)

### Prefer atomic stdlib temporary creation over generating a candidate name

At audit time, `internal/client/project.go` implemented `temporaryPath` with `crypto/rand.Read` plus hexadecimal encoding. It has been removed. The initial implementation used `os.MkdirTemp` and `os.CreateTemp`, but those path-based functions would reopen a confinement race after adopting `os.Root`. The final implementation generates names with `crypto/rand.Text` and atomically creates them with `Root.Mkdir` or `Root.OpenFile` using exclusive creation. [`crypto/rand.Text` documentation](https://pkg.go.dev/crypto/rand@go1.26.5#Text), [`os.Root.OpenFile` documentation](https://pkg.go.dev/os@go1.26.5#Root.OpenFile)

For ordinary path-based code, prefer `MkdirTemp` and `CreateTemp`. Inside an `os.Root` security boundary, use root-relative exclusive creation instead because the standard library does not currently provide `Root.MkdirTemp` or `Root.CreateTemp`. This is a simplification and correctness cleanup, not evidence of a random-name weakness.

### Keep the bounded ZIP preflight unless the archive contract changes

`internal/tree/preflight.go` manually parses the ZIP end record and central-directory headers. This is security-sensitive custom format parsing, but the reason is valid: `archive/zip.NewReader` builds its complete file slice and exposes no entry-count or central-directory allocation limit before doing so. The repository also intentionally rejects ZIP64 and multi-disk input, whereas the standard reader supports ZIP64 and already rejects disk spanning. Replacing the preflight with `archive/zip` alone would remove an input-cost bound and broaden the accepted format. [`archive/zip.Reader` documentation](https://pkg.go.dev/archive/zip@go1.26.5#Reader), [`archive/zip` package documentation](https://pkg.go.dev/archive/zip@go1.26.5)

Retain focused malformed-archive tests and re-check `archive/zip` on future Go upgrades for a pre-parse resource-limit API. The stdlib's insecure-path checks are not a replacement for the repository's portable-tree validation either.

### Prefer `ReverseProxy.Rewrite` over `Director`

Go 1.26 deprecated `httputil.ReverseProxy.Director` in favor of the `Rewrite` hook added in Go 1.20. The release notes call `Director` fundamentally unsafe because a malicious client can use hop-by-hop headers to remove headers added by the director; `Rewrite` receives both the unmodified inbound request and the outbound request after hop-by-hop headers have been removed. [Go 1.26 release notes](https://go.dev/doc/go1.26#net/http/httputil), [`ReverseProxy` documentation](https://pkg.go.dev/net/http/httputil@go1.26.5#ReverseProxy)

There is no `ReverseProxy` use in the current repository. Keep this as a prospective review rule: reject new `Director:` configuration and inspect any custom proxy/header-forwarding code for a standard `ReverseProxy` implementation with `Rewrite`.

### Preserve standard URL parsing and signal cancellation

Go 1.26's `net/url.Parse` now rejects malformed hosts containing unbracketed or repeated colons. The repository's registry-origin constructor already builds on `url.Parse`, which is preferable to string splitting or origin comparison by serialization. Retain explicit application policy checks after parsing (scheme, userinfo, path, query, fragment, and allowed host); the stricter parser does not replace those. [Go 1.26 release notes](https://go.dev/doc/go1.26#net/url), [`url.Parse` documentation](https://pkg.go.dev/net/url@go1.26.5#Parse)

`signal.NotifyContext`, already used by both command entry points, now records the received signal as the context cancellation cause in Go 1.26. Audit code that invents signal channels or cancellation goroutines and prefer `NotifyContext`; downstream diagnostics may now use `context.Cause(ctx)`. [Go 1.26 release notes](https://go.dev/doc/go1.26#os/signal), [`signal.NotifyContext` documentation](https://pkg.go.dev/os/signal@go1.26.5#NotifyContext)

## Lower-priority or non-applicable Go 1.26 changes

- `io.ReadAll` is substantially faster and allocates less in Go 1.26. Existing bounded `io.ReadAll(io.LimitReader(...))` uses should remain bounded; the performance improvement is not permission to remove limits. [Go 1.26 release notes](https://go.dev/doc/go1.26#io)
- `log/slog.NewMultiHandler` can replace a custom fan-out logging handler. The repository has test capture handlers but no production fan-out implementation, so there is currently nothing to replace. [Go 1.26 release notes](https://go.dev/doc/go1.26#log/slog)
- `crypto/hpke`, KEM interfaces, direct HTTP/2 client-connection management, platform process handles, and experimental secret erasure/SIMD do not match current application responsibilities. Do not introduce them merely because they are new. [Go 1.26 release notes](https://go.dev/doc/go1.26#stdlib)

## Suggested audit order

1. Prototype and threat-model replacing `gorilla/csrf` with `http.CrossOriginProtection`; inventory every token, cookie, persisted-key, template, and client-test dependency before changing code.
2. Redesign managed client filesystem mutations around `os.Root`, retaining explicit no-symlink policy checks where required.
3. Replace `temporaryPath` with root-relative, exclusive temporary creation as part of that filesystem work.
4. On a clean branch, run `go fix ./...`, review the patch, then run the repository's full validation suite.
5. Sweep `errors.As` and crypto-random call sites that the modernizers leave behind, preserving protocol formats.
6. Add review/lint rules against new `ReverseProxy.Director` use and hand-parsed origins.
7. Repeat this release-note/API audit whenever the `go` directive advances; `go fix` and release notes are complementary.
