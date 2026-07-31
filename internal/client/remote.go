package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
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
	maxZIPBytes, err := tree.MaxArchiveBytes(limits)
	if err != nil {
		return nil, fmt.Errorf("derive registry archive limit: %w", err)
	}

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
	return protocol.ReadCurrent(response, skill)
}

func (r *remote) fetchTree(ctx context.Context, expected registry.PublicationID) (_ fetchedSkill, err error) {
	response, err := r.get(ctx, r.baseURL.String()+protocol.TreePath(expected), protocol.ZIPMediaType)
	if err != nil {
		return fetchedSkill{}, fmt.Errorf("download publication tree: %w", err)
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if err := protocol.ReadTree(response, expected); err != nil {
		return fetchedSkill{}, err
	}
	archive, err := tree.ReceiveArchive(ctx, r.stagingParent, response.Body, response.ContentLength, r.maxZIPBytes)
	if err != nil {
		if errors.Is(err, tree.ErrInvalid) {
			return fetchedSkill{}, fmt.Errorf("%w: %v", protocol.ErrInvalidResponse, err)
		}
		return fetchedSkill{}, err
	}
	defer func() { err = errors.Join(err, archive.Close()) }()

	snapshot, err := tree.DecodeArchive(ctx, archive, r.stagingParent, r.limits)
	if err != nil {
		if errors.Is(err, tree.ErrInvalid) {
			return fetchedSkill{}, fmt.Errorf("%w: %v", protocol.ErrInvalidResponse, err)
		}
		return fetchedSkill{}, err
	}
	owned := true
	defer func() {
		if owned {
			err = errors.Join(err, snapshot.Close())
		}
	}()
	inspection, err := registry.Inspect(ctx, snapshot, ".")
	if err != nil {
		return fetchedSkill{}, fmt.Errorf("%w: downloaded tree is not an Agent Skill: %v", protocol.ErrInvalidResponse, err)
	}
	if err := inspection.Verify(expected); err != nil {
		return fetchedSkill{}, fmt.Errorf("%w: downloaded tree: %v", protocol.ErrInvalidResponse, err)
	}
	owned = false
	return fetchedSkill{publication: expected, tree: snapshot}, nil
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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
