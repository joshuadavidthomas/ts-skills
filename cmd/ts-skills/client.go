package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

const (
	maxJSONResponseBytes int64 = 64 << 10
	maxErrorMessageBytes       = 200
)

var (
	errProtocol       = protocol.ErrInvalid
	errNotFound       = errors.New("registry value not found")
	errInvalidRequest = errors.New("invalid registry request")
	errInternal       = errors.New("registry internal error")
	errUnavailable    = errors.New("registry temporarily unavailable")
)

// origin is a validated registry base: HTTPS, or loopback HTTP; host only —
// no path, credentials, query, or fragment.
type origin struct{ url url.URL }

// parseOrigin parses and validates a registry origin.
func parseOrigin(text string) (origin, error) {
	source, err := url.Parse(text)
	if err != nil {
		return origin{}, err
	}
	if source.Scheme != "https" && source.Scheme != "http" {
		return origin{}, fmt.Errorf("URL scheme must be HTTPS or loopback HTTP")
	}
	if source.Host == "" || source.Hostname() == "" || source.Opaque != "" {
		return origin{}, fmt.Errorf("URL must have an origin host")
	}
	if source.User != nil || source.RawQuery != "" || source.ForceQuery || source.Fragment != "" {
		return origin{}, fmt.Errorf("URL must not contain user info, a query, or a fragment")
	}
	if (source.Path != "" && source.Path != "/") || (source.RawPath != "" && source.RawPath != "/") {
		return origin{}, fmt.Errorf("URL must not contain a path")
	}
	if source.Scheme == "http" && !isLoopbackHost(source.Hostname()) {
		return origin{}, fmt.Errorf("cleartext HTTP is allowed only for a loopback host")
	}
	source.Path = ""
	source.RawPath = ""
	return origin{url: *source}, nil
}

// asURL returns a fresh URL for consumers of net/url APIs.
func (o origin) asURL() *url.URL {
	clone := o.url
	return &clone
}

type remote struct {
	baseURL       *url.URL
	client        *http.Client
	stagingParent string
	limits        tree.Limits
	maxZIPBytes   int64
}

func newRemote(origin origin, httpClient *http.Client, stagingParent string, limits tree.Limits) (*remote, error) {
	base := origin.asURL()
	if httpClient == nil {
		return nil, fmt.Errorf("registry HTTP client must be provided")
	}
	if httpClient.Timeout <= 0 {
		return nil, fmt.Errorf("registry HTTP client timeout must be positive")
	}
	if err := tree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("registry tree limits: %w", err)
	}
	info, err := os.Stat(stagingParent)
	if err != nil {
		return nil, fmt.Errorf("stat registry staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("registry staging parent must be a directory")
	}
	maxZIPBytes := tree.MaxBytes

	privateClient := *httpClient
	privateClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("registry redirects are not accepted")
	}
	return &remote{
		baseURL: base, client: &privateClient, stagingParent: stagingParent, limits: limits, maxZIPBytes: maxZIPBytes,
	}, nil
}

func (r *remote) fetch(ctx context.Context, requirement requirement) (fetchedSkill, error) {
	if r == nil || r.client == nil || requirement.skillID().String() == "" {
		return fetchedSkill{}, fmt.Errorf("fetch requires a configured registry and valid requirement")
	}
	if ctx == nil {
		return fetchedSkill{}, fmt.Errorf("fetch context must be provided")
	}

	var publication registry.PublicationID
	if digest, exact := requirement.exactDigest(); exact {
		var err error
		publication, err = registry.NewPublicationID(requirement.skillID(), digest)
		if err != nil {
			return fetchedSkill{}, err
		}
	} else {
		var err error
		publication, err = r.resolveCurrent(ctx, requirement.skillID())
		if err != nil {
			return fetchedSkill{}, err
		}
	}
	return r.fetchTree(ctx, publication)
}

func (r *remote) resolveCurrent(ctx context.Context, skill registry.SkillID) (registry.PublicationID, error) {
	response, err := r.get(ctx, r.baseURL.String()+protocol.CurrentPath(skill), protocol.JSONMediaType)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("resolve current publication: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return registry.PublicationID{}, r.responseError(response)
	}
	if err := requireContentType(response.Header.Get("Content-Type"), protocol.JSONMediaType, true); err != nil {
		return registry.PublicationID{}, err
	}
	body, err := readBounded(response.Body, response.ContentLength, maxJSONResponseBytes)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: read current response: %v", errProtocol, err)
	}
	var wire protocol.CurrentResponse
	if err := decodeStrictJSON(body, &wire); err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: decode current response: %v", errProtocol, err)
	}
	publication, err := protocol.ParseCurrentResponse(wire)
	if err != nil {
		return registry.PublicationID{}, err
	}
	if publication.Skill() != skill {
		return registry.PublicationID{}, fmt.Errorf("%w: current response names another skill", errIdentityMismatch)
	}
	return publication, nil
}

func (r *remote) fetchTree(ctx context.Context, expected registry.PublicationID) (_ fetchedSkill, err error) {
	response, err := r.get(ctx, r.baseURL.String()+protocol.TreePath(expected), protocol.ZIPMediaType)
	if err != nil {
		return fetchedSkill{}, fmt.Errorf("download publication tree: %w", err)
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		return fetchedSkill{}, r.responseError(response)
	}
	if err := requireContentType(response.Header.Get("Content-Type"), protocol.ZIPMediaType, false); err != nil {
		return fetchedSkill{}, err
	}
	publication, err := protocol.ParsePublicationHeaders(response.Header)
	if err != nil {
		return fetchedSkill{}, err
	}
	if publication != expected {
		return fetchedSkill{}, fmt.Errorf("%w: tree response identifies another publication", errIdentityMismatch)
	}
	if response.ContentLength > r.maxZIPBytes {
		return fetchedSkill{}, &tree.LimitError{Limit: "download bytes", Max: r.maxZIPBytes, Actual: response.ContentLength}
	}

	spool, err := os.CreateTemp(r.stagingParent, ".ts-skills-download-*.zip")
	if err != nil {
		return fetchedSkill{}, fmt.Errorf("create download staging file: %w", err)
	}
	spoolName := spool.Name()
	defer func() { _ = os.Remove(spoolName) }()
	written, copyErr := io.Copy(spool, io.LimitReader(response.Body, r.maxZIPBytes+1))
	closeErr := spool.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fetchedSkill{}, fmt.Errorf("stage publication archive: %w", err)
	}
	if written > r.maxZIPBytes {
		return fetchedSkill{}, &tree.LimitError{Limit: "download bytes", Max: r.maxZIPBytes, Actual: written}
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return fetchedSkill{}, fmt.Errorf("%w: tree response was truncated", errProtocol)
	}

	snapshot, err := tree.Decode(ctx, spoolName, r.stagingParent, r.limits)
	if err != nil {
		if errors.Is(err, tree.ErrInvalid) {
			return fetchedSkill{}, fmt.Errorf("%w: %v", errProtocol, err)
		}
		return fetchedSkill{}, err
	}
	owned := true
	defer func() {
		if owned {
			err = errors.Join(err, snapshot.Close())
		}
	}()
	inspection, err := registry.Inspect(ctx, snapshot.FS(), ".")
	if err != nil {
		return fetchedSkill{}, fmt.Errorf("%w: downloaded tree is not an Agent Skill: %v", errProtocol, err)
	}
	if err := inspection.RequireName(publication.Skill().Name()); err != nil {
		return fetchedSkill{}, fmt.Errorf("%w: downloaded SKILL.md names another skill", errIdentityMismatch)
	}
	if inspection.Digest() != publication.Tree() {
		return fetchedSkill{}, fmt.Errorf("%w: expected %s, got %s", errDigestMismatch, publication.Tree().String(), inspection.Digest().String())
	}
	owned = false
	return fetchedSkill{publication: publication, tree: &fetchedTree{snapshot: snapshot}}, nil
}

func (r *remote) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (r *remote) responseError(response *http.Response) error {
	if err := requireContentType(response.Header.Get("Content-Type"), protocol.JSONMediaType, true); err != nil {
		return err
	}
	body, err := readBounded(response.Body, response.ContentLength, maxJSONResponseBytes)
	if err != nil {
		return fmt.Errorf("%w: read error response: %v", errProtocol, err)
	}
	var wire protocol.ErrorResponse
	if err := decodeStrictJSON(body, &wire); err != nil {
		return fmt.Errorf("%w: decode error response: %v", errProtocol, err)
	}
	expected, known := protocol.StatusForCode(wire.Code)
	if !known || expected != response.StatusCode || wire.Message == "" {
		return fmt.Errorf("%w: unknown error code or status", errProtocol)
	}
	switch wire.Code {
	case protocol.CodeNotFound:
		return fmt.Errorf("%w: requested publication does not exist", errNotFound)
	case protocol.CodeInvalidRequest:
		return fmt.Errorf("%w: %s", errInvalidRequest, safeErrorMessage(wire.Message, "registry rejected the request"))
	case protocol.CodeTooLarge:
		return fmt.Errorf("%w: registry could not return the tree within its limit", tree.ErrLimitExceeded)
	case protocol.CodeInternal:
		return fmt.Errorf("%w: %s", errInternal, safeErrorMessage(wire.Message, "registry encountered an internal error"))
	case protocol.CodeUnavailable:
		return fmt.Errorf("%w: %s", errUnavailable, safeErrorMessage(wire.Message, "registry is temporarily unavailable"))
	default:
		return fmt.Errorf("%w: unknown registry error", errProtocol)
	}
}

func safeErrorMessage(message, fallback string) string {
	if len(message) > maxErrorMessageBytes || !utf8.ValidString(message) {
		return fallback
	}
	for _, r := range message {
		if unicode.IsControl(r) {
			return fallback
		}
	}
	return message
}

func requireContentType(header, expected string, allowUTF8Charset bool) error {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil || mediaType != expected {
		return fmt.Errorf("%w: expected Content-Type %s", errProtocol, expected)
	}
	if len(parameters) == 0 {
		return nil
	}
	if allowUTF8Charset && len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8") {
		return nil
	}
	return fmt.Errorf("%w: unexpected Content-Type parameters", errProtocol)
}

func readBounded(source io.Reader, contentLength, maximum int64) ([]byte, error) {
	if contentLength > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	contents, err := io.ReadAll(io.LimitReader(source, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	if contentLength >= 0 && int64(len(contents)) != contentLength {
		return nil, io.ErrUnexpectedEOF
	}
	return contents, nil
}

func decodeStrictJSON(source []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("response contains trailing JSON data")
	}
	return nil
}

type fetchedTree struct {
	snapshot      *tree.Snapshot
	closeSnapshot func(*tree.Snapshot) error
}

func (t *fetchedTree) Open(name string) (fs.File, error) {
	return t.snapshot.FS().Open(name)
}

func (t *fetchedTree) Close() error {
	if t == nil || t.snapshot == nil {
		return nil
	}
	closeSnapshot := t.closeSnapshot
	if closeSnapshot == nil {
		closeSnapshot = (*tree.Snapshot).Close
	}
	if err := closeSnapshot(t.snapshot); err != nil {
		return err
	}
	t.snapshot = nil
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
