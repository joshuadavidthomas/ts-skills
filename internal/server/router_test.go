package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	serverweb "github.com/joshuadavidthomas/ts-skills/internal/server/web"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
	"golang.org/x/sync/semaphore"
)

type fixedCuratorResolver struct {
	curator servercatalog.Curator
	err     error
}

func (r *fixedCuratorResolver) resolve(*http.Request) (servercatalog.Curator, error) {
	return r.curator, r.err
}

type webFixture struct {
	t             *testing.T
	server        *httptest.Server
	client        *http.Client
	cookie        *http.Cookie
	token         string
	storage       *servercatalog.Catalog
	state         string
	staging       string
	resolver      *fixedCuratorResolver
	key           csrfKey
	logger        *slog.Logger
	limits        tree.Limits
	bodyCap       int64
	treeWork      *semaphore.Weighted
	treeWorkLimit int
}

func mustUploadBodyCap(t *testing.T, limits tree.Limits) int64 {
	t.Helper()
	cap, err := serverweb.UploadBodyCap(limits)
	if err != nil {
		t.Fatal(err)
	}
	return cap
}

func mustArchiveCap(t *testing.T, limits tree.Limits) int64 {
	t.Helper()
	cap, err := tree.MaxArchiveBytes(limits)
	if err != nil {
		t.Fatal(err)
	}
	return cap
}

func newWebFixture(t *testing.T) *webFixture {
	return newWebFixtureWithTreeWork(t, tree.PrototypeLimits(), nil, 0)
}

func newWebFixtureWithLimits(t *testing.T, limits tree.Limits, logger *slog.Logger) *webFixture {
	return newWebFixtureWithTreeWork(t, limits, logger, 0)
}

func newWebFixtureWithTreeWork(t *testing.T, limits tree.Limits, logger *slog.Logger, treeWork int) *webFixture {
	t.Helper()
	state := t.TempDir()
	records, err := servercatalog.Open(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	catalog := records
	actor, err := servercatalog.NewActor("user:42", "Curator <One>")
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := bytes.Repeat([]byte{0x5a}, 32)
	key, err := newCSRFKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedCuratorResolver{curator: servercatalog.Curator{Actor: actor}}
	bodyCap, err := serverweb.UploadBodyCap(limits)
	if err != nil {
		t.Fatal(err)
	}
	var work *semaphore.Weighted
	if treeWork != 0 {
		work = semaphore.NewWeighted(int64(treeWork))
	}
	handler, err := newHandler(catalog, resolver.resolve, handlerOptions{
		StagingParent: staging, Limits: limits, MaxRequestBodyBytes: bodyCap, MaxTreeWork: treeWork, CSRFKey: key, SecureCookies: false, Logger: logger, treeWork: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Config = newHTTPServer(context.Background(), handler, bodyCap, newHandlerGate(nil))
	server.Start()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(server.URL + "/upload")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	token := response.Header.Get("X-CSRF-Token")
	if token == "" {
		t.Fatalf("CSRF token not found in upload response: %s", body)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	fixture := &webFixture{
		t: t, server: server, client: client, cookie: cookies[0], token: token, storage: records,
		state: state, staging: staging, resolver: resolver, key: key, logger: logger, limits: limits, bodyCap: bodyCap, treeWork: work, treeWorkLimit: treeWork,
	}
	t.Cleanup(func() {
		fixture.server.Close()
		if err := fixture.storage.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})
	return fixture
}

func (f *webFixture) restart() {
	f.t.Helper()
	f.server.Close()
	if err := f.storage.Close(); err != nil {
		f.t.Fatal(err)
	}
	records, err := servercatalog.Open(context.Background(), f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	catalog := records
	var work *semaphore.Weighted
	if f.treeWorkLimit != 0 {
		work = semaphore.NewWeighted(int64(f.treeWorkLimit))
	}
	handler, err := newHandler(catalog, f.resolver.resolve, handlerOptions{
		StagingParent: f.staging, Limits: f.limits, MaxRequestBodyBytes: f.bodyCap, MaxTreeWork: f.treeWorkLimit, CSRFKey: f.key, SecureCookies: false, Logger: f.logger, treeWork: work,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.storage = records
	f.treeWork = work
	f.server = httptest.NewUnstartedServer(nil)
	f.server.Config = newHTTPServer(context.Background(), handler, f.bodyCap, newHandlerGate(nil))
	f.server.Start()
	response, err := f.client.Get(f.server.URL + "/upload")
	if err != nil {
		f.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	cookies := response.Cookies()
	if len(cookies) != 1 || response.Header.Get("X-CSRF-Token") == "" {
		f.t.Fatalf("restarted CSRF response is incomplete")
	}
	f.cookie = cookies[0]
	f.token = response.Header.Get("X-CSRF-Token")
}

type formPart struct {
	name     string
	filename string
	body     []byte
}

func multipartRequest(t *testing.T, target string, parts []formPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		var output io.Writer
		if part.filename == "" {
			created, err := writer.CreateFormField(part.name)
			if err != nil {
				t.Fatal(err)
			}
			output = created
		} else {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, part.name, part.filename))
			header.Set("Content-Type", "application/octet-stream")
			created, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			output = created
		}
		if _, err := output.Write(part.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func skillDirectoryParts(instructions string) []formPart {
	skill := "---\nname: sample\ndescription: Web test\n---\n" + instructions
	asset := "inert asset"
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d},{"index":1,"path":"sample/assets/inert.txt","size":%d}]`, len(skill), len(asset))
	return []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "manifest", body: []byte(manifest)},
		{name: "file-0", filename: "SKILL.md", body: []byte(skill)},
		{name: "file-1", filename: "not-the-path.txt", body: []byte(asset)},
	}
}

func (f *webFixture) do(request *http.Request, csrf bool) *http.Response {
	f.t.Helper()
	request.AddCookie(f.cookie)
	if csrf {
		request.Header.Set("X-CSRF-Token", f.token)
	}
	response, err := f.client.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	return response
}

func mustNewRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func (f *webFixture) uploadDirectory(instructions string) string {
	f.t.Helper()
	request := multipartRequest(f.t, f.server.URL+"/candidates", skillDirectoryParts(instructions))
	response := f.do(request, true)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(response.Body)
		f.t.Fatalf("upload status = %d: %s", response.StatusCode, body)
	}
	return response.Header.Get("Location")
}

func (f *webFixture) get(path string) string {
	f.t.Helper()
	request, err := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	response := f.do(request, false)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		f.t.Fatalf("GET %s status = %d: %s", path, response.StatusCode, body)
	}
	return string(body)
}

func postForm(t *testing.T, fixture *webFixture, path string, values url.Values) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fixture.server.URL+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return fixture.do(request, true)
}

var digestPattern = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

func TestUploadCarriesStagedTreeDigestIntoCandidate(t *testing.T) {
	fixture := newWebFixture(t)
	instructions := "# Submitted once\n"
	location := fixture.uploadDirectory(instructions)
	id, err := servercatalog.ParseCandidateID(strings.TrimPrefix(location, "/candidates/"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := fixture.storage.Candidate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: sample\ndescription: Web test\n---\n" + instructions
	expected, err := registry.SumTree(context.Background(), fstest.MapFS{
		"sample/SKILL.md":         {Data: []byte(skill)},
		"sample/assets/inert.txt": {Data: []byte("inert asset")},
	}, "sample")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Tree != expected {
		t.Fatalf("candidate digest = %s, want submitted digest %s", candidate.Tree, expected)
	}
}

func TestStaticAssetsAreServedAheadOfCatalogCatchAll(t *testing.T) {
	fixture := newWebFixture(t)
	for _, test := range []struct {
		path    string
		content string
	}{
		{path: "/static/style.css", content: "#4b70cc"},
		{path: "/static/upload.js", content: "document.getElementById('upload-form')"},
	} {
		response, err := fixture.client.Get(fixture.server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), test.content) {
			t.Fatalf("GET %s status/body = %d/%s", test.path, response.StatusCode, body)
		}
	}
}

func TestCurationRoutesEscapeReviewPublishAndChangeCurrent(t *testing.T) {
	fixture := newWebFixture(t)
	firstPath := fixture.uploadDirectory("<script>globalThis.pwned = true</script>\n")
	firstReview := fixture.get(firstPath)
	if strings.Contains(firstReview, "<script>globalThis.pwned") {
		t.Fatal("imported SKILL.md rendered as active script")
	}
	for _, escaped := range []string{"&lt;script&gt;globalThis.pwned", `href="?file=assets/inert.txt"`, "Curator &lt;One&gt;"} {
		if !strings.Contains(firstReview, escaped) {
			t.Fatalf("review does not contain escaped %q: %s", escaped, firstReview)
		}
	}
	firstDigest := digestPattern.FindString(firstReview)
	if firstDigest == "" {
		t.Fatal("first digest missing")
	}

	response := postForm(t, fixture, firstPath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	catalog := fixture.get("/")
	if !strings.Contains(catalog, "team/sample") || !strings.Contains(catalog, firstDigest) {
		t.Fatalf("published catalog missing first current: %s", catalog)
	}

	secondPath := fixture.uploadDirectory("Second inert revision.\n")
	secondReview := fixture.get(secondPath)
	secondDigest := digestPattern.FindString(secondReview)
	if secondDigest == "" || secondDigest == firstDigest {
		t.Fatalf("second digest = %q, first = %q", secondDigest, firstDigest)
	}
	response = postForm(t, fixture, secondPath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("second publish status = %d", response.StatusCode)
	}
	if currentCatalog := fixture.get("/"); !strings.Contains(currentCatalog, firstDigest) {
		t.Fatalf("publishing second candidate changed current: %s", currentCatalog)
	}

	response = postForm(t, fixture, "/current", url.Values{"skill": {"team/sample"}, "digest": {secondDigest}})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("set current status = %d", response.StatusCode)
	}
	currentCatalog := fixture.get("/")
	if !strings.Contains(currentCatalog, secondDigest) || strings.Contains(currentCatalog, firstDigest) {
		t.Fatalf("catalog did not change current: %s", currentCatalog)
	}
}

func TestSetCurrentValidatesRequestAndLeavesCurrentUnchanged(t *testing.T) {
	fixture := newWebFixture(t)
	candidatePath := fixture.uploadDirectory("Current selection validation.\n")
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}

	missingDigest := "sha256:" + strings.Repeat("0", 64)
	for name, values := range map[string]url.Values{
		"missing skill":      {"digest": {digest}},
		"missing digest":     {"skill": {"team/sample"}},
		"duplicate skill":    {"skill": {"team/sample", "team/other"}, "digest": {digest}},
		"invalid skill":      {"skill": {"not a skill"}, "digest": {digest}},
		"invalid digest":     {"skill": {"team/sample"}, "digest": {"not-a-digest"}},
		"unpublished digest": {"skill": {"team/sample"}, "digest": {missingDigest}},
	} {
		t.Run(name, func(t *testing.T) {
			response := postForm(t, fixture, "/current", values)
			_ = response.Body.Close()
			want := http.StatusBadRequest
			if name == "unpublished digest" {
				want = http.StatusNotFound
			}
			if response.StatusCode != want {
				t.Fatalf("set current status = %d, want %d", response.StatusCode, want)
			}
		})
	}

	response = fixture.do(mustNewRequest(t, http.MethodGet, fixture.server.URL+"/api/"+protocol.Version+"/skills/team/sample/current", nil), false)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("current publication status = %d, want 200: %s", response.StatusCode, body)
	}
	var current protocol.CurrentResponse
	if err := json.NewDecoder(response.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if current.Digest != digest {
		t.Fatalf("current digest = %q, want %q", current.Digest, digest)
	}
}

func TestAPIReportsNotFound(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.do(mustNewRequest(t, http.MethodGet, fixture.server.URL+"/api/"+protocol.Version+"/skills/team/missing/current", nil), false)
	assertAPIError(t, response, http.StatusNotFound, protocol.CodeNotFound)
}

func TestAPIRoutesAreOutsidePortalCSRF(t *testing.T) {
	fixture := newWebFixture(t)
	request := mustNewRequest(
		t,
		http.MethodPost,
		fixture.server.URL+"/api/"+protocol.Version+"/skills/team/sample/current",
		nil,
	)
	response := fixture.do(request, false)
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusMethodNotAllowed {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("API POST status = %d, want %d: %s", response.StatusCode, http.StatusMethodNotAllowed, body)
	}
}

func assertAPIError(t *testing.T, response *http.Response, wantStatus int, wantCode protocol.Code) {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("API status = %d, want %d: %s", response.StatusCode, wantStatus, body)
	}
	var body protocol.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != wantCode {
		t.Fatalf("API error code = %q, want %q", body.Code, wantCode)
	}
}

func TestNonCuratingIdentityCanReadButCannotMutate(t *testing.T) {
	fixture := newWebFixture(t)
	publishedPath := fixture.uploadDirectory("Published for read-only access.\n")
	digest := digestPattern.FindString(fixture.get(publishedPath))
	response := postForm(t, fixture, publishedPath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish fixture candidate status = %d", response.StatusCode)
	}
	unpublishedPath := fixture.uploadDirectory("Unpublished for permission check.\n")

	fixture.resolver.err = servercatalog.ErrCurationDenied
	readPaths := []string{
		"/",
		"/api/" + protocol.Version + "/skills/team/sample/current",
		"/api/" + protocol.Version + "/skills/team/sample/publications/" + digest + "/tree.zip",
	}
	for _, path := range readPaths {
		fixture.get(path)
	}
	response = fixture.do(mustNewRequest(t, http.MethodGet, fixture.server.URL+unpublishedPath, nil), false)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("candidate review status = %d, want 403: %s", response.StatusCode, body)
	}

	uploadRequest := multipartRequest(t, fixture.server.URL+"/candidates", skillDirectoryParts("Denied upload.\n"))
	mutations := map[string]func() *http.Response{
		"create candidate":  func() *http.Response { return fixture.do(uploadRequest, true) },
		"publish candidate": func() *http.Response { return postForm(t, fixture, unpublishedPath+"/publish", nil) },
		"set current": func() *http.Response {
			return postForm(t, fixture, "/current", url.Values{"skill": {"team/sample"}, "digest": {digest}})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			response := mutate()
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", response.StatusCode, body)
			}
			if !strings.Contains(string(body), "You do not have permission to curate skills") ||
				!strings.Contains(string(body), "joshuadavidthomas.com/cap/ts-skills") {
				t.Fatalf("permission error is missing guidance: %s", body)
			}
		})
	}
}

func TestResponsesSetDefensiveHeaders(t *testing.T) {
	fixture := newWebFixture(t)
	response := fixture.do(mustNewRequest(t, http.MethodGet, fixture.server.URL+"/upload", nil), false)
	_ = response.Body.Close()
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := response.Header.Get(header); got != want {
			t.Errorf("upload %s = %q, want %q", header, got, want)
		}
	}

	candidatePath := fixture.uploadDirectory("Header test.\n")
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response = postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	response = fixture.do(mustNewRequest(t, http.MethodGet, fixture.server.URL+"/api/"+protocol.Version+"/skills/team/sample/publications/"+digest+"/tree.zip", nil), false)
	_ = response.Body.Close()
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("ZIP X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestCurationResolverFailureIsUnauthorized(t *testing.T) {
	fixture := newWebFixture(t)
	fixture.resolver.err = errors.New("Tailnet LocalAPI unavailable")

	response := fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", skillDirectoryParts("Denied upload.\n")), true)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "Identity could not be verified") {
		t.Fatalf("identity error is missing guidance: %s", body)
	}
}

func TestPublicationTreeRouteReturnsRootlessZIPWithResolvedIdentity(t *testing.T) {
	fixture := newWebFixture(t)
	candidatePath := fixture.uploadDirectory("Download route.\n")
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}

	request, err := http.NewRequest(
		http.MethodGet,
		fixture.server.URL+"/api/"+protocol.Version+"/skills/team/sample/publications/"+digest+"/tree.zip",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response = fixture.do(request, false)
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tree status = %d: %s", response.StatusCode, body)
	}
	for header, want := range map[string]string{
		protocol.HeaderPublicationNamespace: "team",
		protocol.HeaderPublicationName:      "sample",
		protocol.HeaderPublicationDigest:    digest,
	} {
		if got := response.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	ceiling := mustArchiveCap(t, tree.PrototypeLimits())
	if int64(len(body)) > ceiling {
		t.Fatalf("tree ZIP bytes = %d, exceeds protocol ceiling %d", len(body), ceiling)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range archive.File {
		if file.Method != zip.Store {
			t.Fatalf("tree ZIP entry %q method = %d, want %d", file.Name, file.Method, zip.Store)
		}
		names = append(names, file.Name)
	}
	wantNames := []string{"SKILL.md", "assets/inert.txt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("tree ZIP entries = %v, want %v", names, wantNames)
	}
	entries, err := os.ReadDir(fixture.staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging entries after tree download = %v, want none", entries)
	}
}

func TestPublishedCurationSurvivesStorageAndHandlerRestart(t *testing.T) {
	fixture := newWebFixture(t)
	candidatePath := fixture.uploadDirectory("Persist across restart.\n")
	review := fixture.get(candidatePath)
	digest := digestPattern.FindString(review)
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}

	fixture.restart()
	catalog := fixture.get("/")
	if !strings.Contains(catalog, "team/sample") || !strings.Contains(catalog, digest) {
		t.Fatalf("restarted catalog lost publication: %s", catalog)
	}
	restartedReview := fixture.get(candidatePath)
	if !strings.Contains(restartedReview, "Persist across restart.") || !strings.Contains(restartedReview, ">Published<") {
		t.Fatalf("restarted review lost candidate facts: %s", restartedReview)
	}
}

func TestSkillDetailPageShowsCurrentPublication(t *testing.T) {
	fixture := newWebFixture(t)
	candidatePath := fixture.uploadDirectory("Detail page instructions.\n")
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}

	catalog := fixture.get("/")
	if !strings.Contains(catalog, `href="/skills/team/sample"`) {
		t.Fatalf("catalog does not link to the skill page: %s", catalog)
	}
	detail := fixture.get("/skills/team/sample")
	for _, want := range []string{"team/sample", digest, "Detail page instructions.", "SKILL.md", "/publications/" + digest + "/tree.zip"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("skill page is missing %q: %s", want, detail)
		}
	}

	missing, err := fixture.client.Get(fixture.server.URL + "/skills/team/unknown")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, missing.Body)
	_ = missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown skill status = %d", missing.StatusCode)
	}
}

func TestSkillLinksEscapeNamespaceSegments(t *testing.T) {
	fixture := newWebFixture(t)
	parts := skillDirectoryParts("Escaped namespace links.\n")
	parts[0].body = []byte("team?x")
	response := fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", parts), true)
	candidatePath := response.Header.Get("Location")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload status = %d", response.StatusCode)
	}
	response = postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}

	const skillPath = "/skills/team%3Fx/sample"
	catalog := fixture.get("/")
	if !strings.Contains(catalog, `href="`+skillPath+`"`) {
		t.Fatalf("catalog link = %s", catalog)
	}
	review := fixture.get(candidatePath)
	if !strings.Contains(review, `href="`+skillPath+`"`) {
		t.Fatalf("review link = %s", review)
	}
	detail := fixture.get(skillPath)
	if !strings.Contains(detail, "/api/"+protocol.Version+"/skills/team%3Fx/sample/publications/") {
		t.Fatalf("download link = %s", detail)
	}
}

func TestReviewAndSkillPagesSelectExactTreeFiles(t *testing.T) {
	fixture := newWebFixture(t)
	candidatePath := fixture.uploadDirectory("Default file content.\n")

	assertFilePages := func(t *testing.T, pagePath string) {
		t.Helper()
		defaultPage := fixture.get(pagePath)
		if !strings.Contains(defaultPage, "Default file content.") || !strings.Contains(defaultPage, `>SKILL.md</h3>`) {
			t.Fatalf("default page does not show SKILL.md: %s", defaultPage)
		}
		if !strings.Contains(defaultPage, `<span class="block py-1 font-medium text-gray-600">assets/</span>`) ||
			!strings.Contains(defaultPage, `href="?file=assets/inert.txt"`) {
			t.Fatalf("page does not show the nested asset link: %s", defaultPage)
		}

		selectedPath := pagePath + "?file=" + url.QueryEscape("assets/inert.txt")
		selectedPage := fixture.get(selectedPath)
		if !strings.Contains(selectedPage, "inert asset") || !strings.Contains(selectedPage, `>assets/inert.txt</h3>`) {
			t.Fatalf("selected asset is not shown in the content pane: %s", selectedPage)
		}

		for _, file := range []string{"nope.txt", "assets", ""} {
			request, err := http.NewRequest(http.MethodGet, fixture.server.URL+pagePath+"?file="+url.QueryEscape(file), nil)
			if err != nil {
				t.Fatal(err)
			}
			response := fixture.do(request, false)
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "File was not found") {
				t.Fatalf("select %q status/body = %d/%s", file, response.StatusCode, body)
			}
		}
	}

	t.Run("candidate review", func(t *testing.T) {
		assertFilePages(t, candidatePath)
	})

	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	t.Run("skill detail", func(t *testing.T) {
		assertFilePages(t, "/skills/team/sample")
	})
}

func TestReviewAndSkillPagesDescribeBinaryFiles(t *testing.T) {
	fixture := newWebFixture(t)
	skill := "---\nname: sample\ndescription: Binary web test\n---\nDefault text.\n"
	binary := []byte{0xff, 0xfe, 0x00}
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d},{"index":1,"path":"sample/assets/data.bin","size":%d}]`, len(skill), len(binary))
	request := multipartRequest(t, fixture.server.URL+"/candidates", []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "manifest", body: []byte(manifest)},
		{name: "file-0", filename: "SKILL.md", body: []byte(skill)},
		{name: "file-1", filename: "data.bin", body: binary},
	})
	response := fixture.do(request, true)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload status = %d", response.StatusCode)
	}
	candidatePath := response.Header.Get("Location")
	selectedQuery := "?file=" + url.QueryEscape("assets/data.bin")
	assertBinary := func(t *testing.T, pagePath string) {
		t.Helper()
		page := fixture.get(pagePath + selectedQuery)
		if !strings.Contains(page, `>assets/data.bin</h3>`) ||
			!strings.Contains(page, "Binary file — download the ZIP to view it.") {
			t.Fatalf("binary file fallback is missing: %s", page)
		}
	}

	assertBinary(t, candidatePath)
	response = postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	assertBinary(t, "/skills/team/sample")
}

func TestReviewAndSkillPagesTruncateLargePreviews(t *testing.T) {
	const previewByteLimit = 256 << 10
	fixture := newWebFixture(t)
	skill := "---\nname: sample\ndescription: Preview size test\n---\nDefault text.\n"
	large := []byte(strings.Repeat("x", previewByteLimit+1))
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d},{"index":1,"path":"sample/assets/large.txt","size":%d}]`, len(skill), len(large))
	request := multipartRequest(t, fixture.server.URL+"/candidates", []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "manifest", body: []byte(manifest)},
		{name: "file-0", filename: "SKILL.md", body: []byte(skill)},
		{name: "file-1", filename: "large.txt", body: large},
	})
	response := fixture.do(request, true)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload status = %d", response.StatusCode)
	}
	candidatePath := response.Header.Get("Location")
	assertPreview := func(t *testing.T, path string) {
		t.Helper()
		page := fixture.get(path + "?file=assets/large.txt")
		if !strings.Contains(page, "Preview truncated — download the ZIP for the full file.") {
			t.Fatalf("truncation notice is missing: %s", page)
		}
		if len(page) > previewByteLimit+(10<<10) {
			t.Fatalf("rendered preview is %d bytes, want at most %d", len(page), previewByteLimit+(10<<10))
		}
	}

	assertPreview(t, candidatePath)
	response = postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	assertPreview(t, "/skills/team/sample")
}

func TestReadRoutesRejectWhenTreeWorkIsSaturated(t *testing.T) {
	fixture := newWebFixtureWithTreeWork(t, tree.PrototypeLimits(), nil, 1)
	candidatePath := fixture.uploadDirectory("Bounded read work.\n")
	digest := digestPattern.FindString(fixture.get(candidatePath))
	response := postForm(t, fixture, candidatePath+"/publish", nil)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("publish status = %d", response.StatusCode)
	}
	if !fixture.treeWork.TryAcquire(1) {
		t.Fatal("failed to saturate tree work")
	}
	defer fixture.treeWork.Release(1)

	for name, path := range map[string]string{
		"preview":  candidatePath,
		"download": "/api/" + protocol.Version + "/skills/team/sample/publications/" + digest + "/tree.zip",
	} {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, fixture.server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response := fixture.do(request, false)
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "1" {
				t.Fatalf("saturated route status/retry-after = %d/%q, want 503/1: %s", response.StatusCode, response.Header.Get("Retry-After"), body)
			}
		})
	}
}

func TestUploadLimitMapsToRequestEntityTooLarge(t *testing.T) {
	fixture := newWebFixture(t)
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d}]`, (16<<20)+1)
	request := multipartRequest(t, fixture.server.URL+"/candidates", []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "manifest", body: []byte(manifest)},
	})
	response := fixture.do(request, true)
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "Upload is too large") {
		t.Fatalf("limit status/body = %d/%s", response.StatusCode, body)
	}
}

func TestUploadBodyCapLetsTreeLimitsDecideAtSmallScale(t *testing.T) {
	limits := tree.Limits{
		MaxFiles:         1,
		MaxPathBytes:     128,
		MaxDepth:         2,
		MaxFileBytes:     4096,
		MaxExpandedBytes: 4096,
	}
	fixture := newWebFixtureWithLimits(t, limits, nil)
	makeParts := func(size int) []formPart {
		body := append([]byte("---\nname: sample\ndescription: Request body cap test\n---\n"), bytes.Repeat([]byte("x"), size-len("---\nname: sample\ndescription: Request body cap test\n---\n"))...)
		manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d}]`, len(body))
		return []formPart{
			{name: "namespace", body: []byte("team")},
			{name: "manifest", body: []byte(manifest)},
			{name: "file-0", filename: "SKILL.md", body: body},
		}
	}

	accepted := multipartRequest(t, fixture.server.URL+"/candidates", makeParts(4095))
	if accepted.ContentLength >= fixture.bodyCap {
		t.Fatalf("accepted request is %d bytes, body cap is %d", accepted.ContentLength, fixture.bodyCap)
	}
	response := fixture.do(accepted, true)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("near-limit upload status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}

	rejected := multipartRequest(t, fixture.server.URL+"/candidates", makeParts(4097))
	if rejected.ContentLength >= fixture.bodyCap {
		t.Fatalf("over-tree-limit request is %d bytes, body cap is %d", rejected.ContentLength, fixture.bodyCap)
	}
	response = fixture.do(rejected, true)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("over-tree-limit upload status = %d, want %d: %s", response.StatusCode, http.StatusRequestEntityTooLarge, body)
	}
}

func TestUploadRequiresCSRFAndRejectsExtraParts(t *testing.T) {
	fixture := newWebFixture(t)
	validParts := skillDirectoryParts("Safe.\n")
	response := fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", validParts), false)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", response.StatusCode)
	}

	extra := append(append([]formPart{}, validParts...), formPart{name: "extra", body: []byte("x")})
	response = fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", extra), true)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("extra part status = %d", response.StatusCode)
	}
}

// recordSlogHandler captures log records so tests can assert that
// unexpected and post-commit failures reach the operator log.
type recordSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordSlogHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record)
	return nil
}

func (h *recordSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordSlogHandler) WithGroup(string) slog.Handler      { return h }

// contains reports whether any captured record carries the text in its
// message or one of its attribute values.
func (h *recordSlogHandler) contains(want string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.records {
		if strings.Contains(record.Message, want) {
			return true
		}
		found := false
		record.Attrs(func(attr slog.Attr) bool {
			if strings.Contains(attr.Value.String(), want) {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func TestNewHandlerDefaultsLogger(t *testing.T) {
	captured := &recordSlogHandler{}
	restore := slog.Default()
	slog.SetDefault(slog.New(captured))
	defer slog.SetDefault(restore)

	key, err := newCSRFKey(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := servercatalog.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	resolver := &fixedCuratorResolver{}
	handler, err := newHandler(catalog, resolver.resolve, handlerOptions{
		StagingParent: t.TempDir(), Limits: tree.PrototypeLimits(), MaxRequestBodyBytes: mustUploadBodyCap(t, tree.PrototypeLimits()), CSRFKey: key, SecureCookies: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !captured.contains("registry storage is closed") {
		t.Error("nil option logger did not fall through to slog.Default()")
	}
}

func TestNewHandlerValidatesCSRFAndOptions(t *testing.T) {
	if _, err := newCSRFKey(make([]byte, 31)); err == nil {
		t.Fatal("short CSRF key accepted")
	}
	if _, err := newCSRFKey(make([]byte, 32)); err == nil {
		t.Fatal("zero CSRF key accepted")
	}
	key, err := newCSRFKey(bytes.Repeat([]byte{1}, 32))
	if err != nil || key == (csrfKey{}) {
		t.Fatalf("valid key = %x, %v", key, err)
	}
}

func TestNewHandlerRejectsRequestBodyCapBelowUploadMinimum(t *testing.T) {
	limits := tree.PrototypeLimits()
	minimum := mustUploadBodyCap(t, limits)
	catalog, err := servercatalog.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	key, err := newCSRFKey(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = newHandler(catalog, (&fixedCuratorResolver{}).resolve, handlerOptions{
		StagingParent: t.TempDir(), Limits: limits, MaxRequestBodyBytes: minimum - 1, CSRFKey: key,
	})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d", minimum-1)) || !strings.Contains(err.Error(), fmt.Sprintf("%d", minimum)) {
		t.Fatalf("undersized request body cap error = %v, want both cap values", err)
	}
}
