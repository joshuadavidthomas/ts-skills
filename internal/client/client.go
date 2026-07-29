package client

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/install"
	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

const maxJSONResponseBytes int64 = 64 << 10

// Origin is a validated registry base: HTTPS, or loopback HTTP; host only —
// no path, credentials, query, or fragment.
type Origin struct{ url url.URL }

// ParseOrigin parses and validates a registry origin.
func ParseOrigin(text string) (Origin, error) {
	source, err := url.Parse(text)
	if err != nil {
		return Origin{}, err
	}
	if source.Scheme != "https" && source.Scheme != "http" {
		return Origin{}, fmt.Errorf("URL scheme must be HTTPS or loopback HTTP")
	}
	if source.Host == "" || source.Hostname() == "" || source.Opaque != "" {
		return Origin{}, fmt.Errorf("URL must have an origin host")
	}
	if source.User != nil || source.RawQuery != "" || source.ForceQuery || source.Fragment != "" {
		return Origin{}, fmt.Errorf("URL must not contain user info, a query, or a fragment")
	}
	if (source.Path != "" && source.Path != "/") || (source.RawPath != "" && source.RawPath != "/") {
		return Origin{}, fmt.Errorf("URL must not contain a path")
	}
	if source.Scheme == "http" && !isLoopbackHost(source.Hostname()) {
		return Origin{}, fmt.Errorf("cleartext HTTP is allowed only for a loopback host")
	}
	source.Path = ""
	source.RawPath = ""
	return Origin{url: *source}, nil
}

// URL returns a fresh URL for consumers of net/url APIs.
func (o Origin) URL() *url.URL {
	clone := o.url
	return &clone
}

func (o Origin) String() string { return o.url.String() }

type Remote struct {
	baseURL       *url.URL
	client        *http.Client
	stagingParent string
	limits        safetree.Limits
	maxZIPBytes   int64
}

func NewRemote(origin Origin, httpClient *http.Client, stagingParent string, limits safetree.Limits) (*Remote, error) {
	base := origin.URL()
	if httpClient == nil {
		return nil, fmt.Errorf("registry HTTP client must be provided")
	}
	if httpClient.Timeout <= 0 {
		return nil, fmt.Errorf("registry HTTP client timeout must be positive")
	}
	if err := safetree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("registry tree limits: %w", err)
	}
	info, err := os.Stat(stagingParent)
	if err != nil {
		return nil, fmt.Errorf("stat registry staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("registry staging parent must be a directory")
	}
	maxZIPBytes, err := protocol.TreeArchiveCeiling(limits)
	if err != nil {
		return nil, fmt.Errorf("registry tree limits: %w", err)
	}

	privateClient := *httpClient
	privateClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("registry redirects are not accepted")
	}
	return &Remote{
		baseURL: base, client: &privateClient, stagingParent: stagingParent, limits: limits, maxZIPBytes: maxZIPBytes,
	}, nil
}

func (r *Remote) Fetch(ctx context.Context, requirement install.Requirement) (install.FetchedSkill, error) {
	if r == nil || r.client == nil || requirement.Skill().String() == "" {
		return install.FetchedSkill{}, fmt.Errorf("fetch requires a configured registry and valid requirement")
	}
	if ctx == nil {
		return install.FetchedSkill{}, fmt.Errorf("fetch context must be provided")
	}

	var publication agentskill.PublicationID
	if digest, exact := requirement.ExactDigest(); exact {
		var err error
		publication, err = agentskill.NewPublicationID(requirement.Skill(), digest)
		if err != nil {
			return install.FetchedSkill{}, err
		}
	} else {
		var err error
		publication, err = r.resolveCurrent(ctx, requirement.Skill())
		if err != nil {
			return install.FetchedSkill{}, err
		}
	}
	return r.fetchTree(ctx, publication)
}

func (r *Remote) resolveCurrent(ctx context.Context, skill agentskill.SkillID) (agentskill.PublicationID, error) {
	endpoint := r.endpoint(
		"api", protocol.Version, "skills", skill.Namespace().String(), skill.Name().String(), "current",
	)
	response, err := r.get(ctx, endpoint, "application/json")
	if err != nil {
		return agentskill.PublicationID{}, fmt.Errorf("resolve current publication: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return agentskill.PublicationID{}, r.responseError(response)
	}
	if err := requireContentType(response.Header.Get("Content-Type"), "application/json", true); err != nil {
		return agentskill.PublicationID{}, err
	}
	body, err := readBounded(response.Body, response.ContentLength, maxJSONResponseBytes)
	if err != nil {
		return agentskill.PublicationID{}, fmt.Errorf("%w: read current response: %v", protocol.ErrProtocol, err)
	}
	var wire protocol.CurrentResponse
	if err := decodeStrictJSON(body, &wire); err != nil {
		return agentskill.PublicationID{}, fmt.Errorf("%w: decode current response: %v", protocol.ErrProtocol, err)
	}
	namespace, err := agentskill.ParseNamespace(wire.Namespace)
	if err != nil || namespace.String() != wire.Namespace {
		return agentskill.PublicationID{}, fmt.Errorf("%w: current response has a noncanonical namespace", protocol.ErrProtocol)
	}
	name, err := agentskill.ParseName(wire.Name)
	if err != nil || name.String() != wire.Name {
		return agentskill.PublicationID{}, fmt.Errorf("%w: current response has a noncanonical Agent Skill name", protocol.ErrProtocol)
	}
	responseSkill, err := agentskill.NewSkillID(namespace, name)
	if err != nil {
		return agentskill.PublicationID{}, fmt.Errorf("%w: current response identity: %v", protocol.ErrProtocol, err)
	}
	if responseSkill != skill {
		return agentskill.PublicationID{}, fmt.Errorf("%w: current response names another skill", install.ErrIdentityMismatch)
	}
	digest, err := agentskill.ParseTreeDigest(wire.Digest)
	if err != nil || digest.String() != wire.Digest {
		return agentskill.PublicationID{}, fmt.Errorf("%w: current response has an invalid digest", protocol.ErrProtocol)
	}
	return agentskill.NewPublicationID(responseSkill, digest)
}

func (r *Remote) fetchTree(ctx context.Context, expected agentskill.PublicationID) (_ install.FetchedSkill, err error) {
	requestedSkill := expected.Skill()
	endpoint := r.endpoint(
		"api", protocol.Version, "skills", requestedSkill.Namespace().String(), requestedSkill.Name().String(),
		"publications", expected.Tree().String(), "tree.zip",
	)
	response, err := r.get(ctx, endpoint, "application/zip")
	if err != nil {
		return install.FetchedSkill{}, fmt.Errorf("download publication tree: %w", err)
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		return install.FetchedSkill{}, r.responseError(response)
	}
	if err := requireContentType(response.Header.Get("Content-Type"), "application/zip", false); err != nil {
		return install.FetchedSkill{}, err
	}
	publication, err := parseTreePublication(response.Header)
	if err != nil {
		return install.FetchedSkill{}, err
	}
	if publication != expected {
		return install.FetchedSkill{}, fmt.Errorf("%w: tree response identifies another publication", install.ErrIdentityMismatch)
	}
	if response.ContentLength > r.maxZIPBytes {
		return install.FetchedSkill{}, &safetree.LimitError{Limit: "download bytes", Max: r.maxZIPBytes, Actual: response.ContentLength}
	}

	spool, err := os.CreateTemp(r.stagingParent, ".ts-skills-download-*.zip")
	if err != nil {
		return install.FetchedSkill{}, fmt.Errorf("create download staging file: %w", err)
	}
	spoolName := spool.Name()
	defer func() { _ = os.Remove(spoolName) }()
	written, copyErr := io.Copy(spool, io.LimitReader(response.Body, r.maxZIPBytes+1))
	closeErr := spool.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return install.FetchedSkill{}, fmt.Errorf("stage publication archive: %w", err)
	}
	if written > r.maxZIPBytes {
		return install.FetchedSkill{}, &safetree.LimitError{Limit: "download bytes", Max: r.maxZIPBytes, Actual: written}
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return install.FetchedSkill{}, fmt.Errorf("%w: tree response was truncated", protocol.ErrProtocol)
	}

	snapshot, err := r.decodeZIP(ctx, spoolName)
	if err != nil {
		return install.FetchedSkill{}, err
	}
	owned := true
	defer func() {
		if owned {
			err = errors.Join(err, snapshot.Close())
		}
	}()
	inspection, err := agentskill.Inspect(ctx, snapshot.FS(), ".")
	if err != nil {
		return install.FetchedSkill{}, fmt.Errorf("%w: downloaded tree is not an Agent Skill: %v", protocol.ErrProtocol, err)
	}
	if err := inspection.RequireName(publication.Skill().Name()); err != nil {
		return install.FetchedSkill{}, fmt.Errorf("%w: downloaded SKILL.md names another skill", install.ErrIdentityMismatch)
	}
	if inspection.Digest() != publication.Tree() {
		return install.FetchedSkill{}, fmt.Errorf("%w: expected %s, got %s", install.ErrDigestMismatch, publication.Tree().String(), inspection.Digest().String())
	}
	owned = false
	return install.FetchedSkill{Publication: publication, Tree: &fetchedTree{snapshot: snapshot}}, nil
}

func parseTreePublication(header http.Header) (agentskill.PublicationID, error) {
	namespaceText, err := requiredTreeHeader(header, protocol.HeaderPublicationNamespace)
	if err != nil {
		return agentskill.PublicationID{}, err
	}
	nameText, err := requiredTreeHeader(header, protocol.HeaderPublicationName)
	if err != nil {
		return agentskill.PublicationID{}, err
	}
	digestText, err := requiredTreeHeader(header, protocol.HeaderPublicationDigest)
	if err != nil {
		return agentskill.PublicationID{}, err
	}

	namespace, err := agentskill.ParseNamespace(namespaceText)
	if err != nil || namespace.String() != namespaceText {
		return agentskill.PublicationID{}, fmt.Errorf("%w: tree response has a noncanonical publication namespace", protocol.ErrProtocol)
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil || name.String() != nameText {
		return agentskill.PublicationID{}, fmt.Errorf("%w: tree response has a noncanonical publication name", protocol.ErrProtocol)
	}
	skill, err := agentskill.NewSkillID(namespace, name)
	if err != nil {
		return agentskill.PublicationID{}, fmt.Errorf("%w: tree response publication identity: %v", protocol.ErrProtocol, err)
	}
	digest, err := agentskill.ParseTreeDigest(digestText)
	if err != nil || digest.String() != digestText {
		return agentskill.PublicationID{}, fmt.Errorf("%w: tree response has a noncanonical publication digest", protocol.ErrProtocol)
	}
	publication, err := agentskill.NewPublicationID(skill, digest)
	if err != nil {
		return agentskill.PublicationID{}, fmt.Errorf("%w: tree response publication identity: %v", protocol.ErrProtocol, err)
	}
	return publication, nil
}

func requiredTreeHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("%w: tree response requires exactly one %s header", protocol.ErrProtocol, name)
	}
	return values[0], nil
}

func (r *Remote) decodeZIP(ctx context.Context, archivePath string) (_ *safetree.Snapshot, err error) {
	maximumEntries := int64(r.limits.MaxFiles)
	if err := preflightZIP(archivePath, maximumEntries); err != nil {
		if errors.Is(err, safetree.ErrLimitExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: inspect tree archive end record: %v", protocol.ErrProtocol, err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("%w: open tree archive: %v", protocol.ErrProtocol, err)
	}
	defer func() { err = errors.Join(err, archive.Close()) }()
	if int64(len(archive.File)) > maximumEntries {
		return nil, &safetree.LimitError{Limit: "archive entries", Max: maximumEntries, Actual: int64(len(archive.File))}
	}
	builder, err := safetree.NewBuilder(r.stagingParent, r.limits)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, builder.Close())
		}
	}()
	for _, entry := range archive.File {
		if entry.Method != protocol.TreeArchiveZIPMethod || entry.Flags&0x1 != 0 || entry.FileInfo().IsDir() || !entry.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: tree archive contains an unsupported entry", protocol.ErrProtocol)
		}
		if entry.UncompressedSize64 > math.MaxInt64 {
			return nil, &safetree.LimitError{Limit: "file bytes", Max: r.limits.MaxFileBytes, Actual: math.MaxInt64}
		}
		input, openErr := entry.Open()
		if openErr != nil {
			return nil, fmt.Errorf("%w: open tree archive entry: %v", protocol.ErrProtocol, openErr)
		}
		addErr := builder.AddFile(ctx, entry.Name, int64(entry.UncompressedSize64), input)
		closeErr := input.Close()
		if addErr != nil {
			switch {
			case errors.Is(addErr, safetree.ErrLimitExceeded):
				return nil, addErr
			case errors.Is(addErr, context.Canceled), errors.Is(addErr, context.DeadlineExceeded):
				return nil, addErr
			default:
				return nil, fmt.Errorf("%w: unsafe tree archive entry: %w", protocol.ErrProtocol, addErr)
			}
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%w: close tree archive entry: %w", protocol.ErrProtocol, closeErr)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *Remote) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
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

func (r *Remote) responseError(response *http.Response) error {
	if err := requireContentType(response.Header.Get("Content-Type"), "application/json", true); err != nil {
		return err
	}
	body, err := readBounded(response.Body, response.ContentLength, maxJSONResponseBytes)
	if err != nil {
		return fmt.Errorf("%w: read error response: %v", protocol.ErrProtocol, err)
	}
	var wire protocol.ErrorResponse
	if err := decodeStrictJSON(body, &wire); err != nil {
		return fmt.Errorf("%w: decode error response: %v", protocol.ErrProtocol, err)
	}
	expected, known := protocol.StatusForCode(wire.Code)
	if !known || expected != response.StatusCode || wire.Message == "" {
		return fmt.Errorf("%w: unknown error code or status", protocol.ErrProtocol)
	}
	switch wire.Code {
	case protocol.CodeNotFound:
		return fmt.Errorf("%w: requested publication does not exist", protocol.ErrNotFound)
	case protocol.CodeInvalidRequest:
		return fmt.Errorf("%w: %s", protocol.ErrInvalidRequest, wire.Message)
	case protocol.CodeTooLarge:
		return fmt.Errorf("%w: registry could not return the tree within its limit", safetree.ErrLimitExceeded)
	case protocol.CodeInternal:
		return fmt.Errorf("%w: %s", protocol.ErrInternal, wire.Message)
	default:
		return fmt.Errorf("%w: unknown registry error", protocol.ErrProtocol)
	}
}

func (r *Remote) endpoint(parts ...string) string {
	escaped := make([]string, len(parts))
	for index, part := range parts {
		escaped[index] = url.PathEscape(part)
	}
	return r.baseURL.String() + "/" + strings.Join(escaped, "/")
}

func requireContentType(header, expected string, allowUTF8Charset bool) error {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil || mediaType != expected {
		return fmt.Errorf("%w: expected Content-Type %s", protocol.ErrProtocol, expected)
	}
	if len(parameters) == 0 {
		return nil
	}
	if allowUTF8Charset && len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8") {
		return nil
	}
	return fmt.Errorf("%w: unexpected Content-Type parameters", protocol.ErrProtocol)
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
	snapshot      *safetree.Snapshot
	closeSnapshot func(*safetree.Snapshot) error
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
		closeSnapshot = (*safetree.Snapshot).Close
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
