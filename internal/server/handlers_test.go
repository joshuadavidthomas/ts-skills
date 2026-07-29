package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
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

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

type fixedCuratorResolver struct {
	curator curator
	err     error
}

func (r *fixedCuratorResolver) resolve(*http.Request) (curator, error) {
	return r.curator, r.err
}

type webFixture struct {
	t        *testing.T
	server   *httptest.Server
	client   *http.Client
	cookie   *http.Cookie
	token    string
	storage  *catalog
	state    string
	staging  string
	resolver *fixedCuratorResolver
	key      csrfKey
	logger   *slog.Logger
}

func newWebFixture(t *testing.T) *webFixture {
	return newWebFixtureWithLogger(t, nil)
}

func newWebFixtureWithLogger(t *testing.T, logger *slog.Logger) *webFixture {
	t.Helper()
	state := t.TempDir()
	records, err := openCatalog(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	catalog := records
	actor := actor{ID: "user:42", Display: "Curator <One>"}
	keyBytes := bytes.Repeat([]byte{0x5a}, 32)
	key, err := newCSRFKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedCuratorResolver{curator: curator{Actor: actor}}
	handler, err := newHandler(catalog, resolver.resolve, handlerOptions{
		StagingParent: staging, Limits: safetree.PrototypeLimits(), CSRFKey: key, SecureCookies: false, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Config = newHTTPServer(context.Background(), handler, newHandlerGate(nil))
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
		state: state, staging: staging, resolver: resolver, key: key, logger: logger,
	}
	t.Cleanup(func() {
		fixture.server.Close()
		if err := fixture.storage.close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})
	return fixture
}

func (f *webFixture) restart() {
	f.t.Helper()
	f.server.Close()
	if err := f.storage.close(); err != nil {
		f.t.Fatal(err)
	}
	records, err := openCatalog(context.Background(), f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	catalog := records
	handler, err := newHandler(catalog, f.resolver.resolve, handlerOptions{
		StagingParent: f.staging, Limits: safetree.PrototypeLimits(), CSRFKey: f.key, SecureCookies: false, Logger: f.logger,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.storage = records
	f.server = httptest.NewUnstartedServer(nil)
	f.server.Config = newHTTPServer(context.Background(), handler, newHandlerGate(nil))
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
	id, err := agentskill.ParseCandidateID(strings.TrimPrefix(location, "/candidates/"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := fixture.storage.candidate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: sample\ndescription: Web test\n---\n" + instructions
	expected, err := agentskill.SumTree(context.Background(), fstest.MapFS{
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

func TestUploadRedirectsWhenPostCommitCleanupFails(t *testing.T) {
	captured := &recordSlogHandler{}
	fixture := newWebFixtureWithLogger(t, slog.New(captured))
	var once sync.Once
	fixture.storage.afterFilesystemStep = func(string) error {
		var err error
		once.Do(func() { err = os.Chmod(fixture.staging, 0o500) })
		return err
	}
	defer func() {
		fixture.storage.afterFilesystemStep = nil
		if err := os.Chmod(fixture.staging, 0o700); err != nil {
			t.Errorf("restore staging permissions: %v", err)
		}
	}()

	location := fixture.uploadDirectory("Cleanup error must not hide the candidate.\n")
	id, err := agentskill.ParseCandidateID(strings.TrimPrefix(location, "/candidates/"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.storage.candidate(context.Background(), id); err != nil {
		t.Fatalf("candidate was not committed before cleanup failed: %v", err)
	}
	if !captured.contains("web upload cleanup failed") {
		t.Fatal("post-commit cleanup failure was not logged")
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

	fixture.resolver.err = errCurationDenied
	readPaths := []string{
		"/",
		unpublishedPath,
		"/api/" + apiVersion + "/skills/team/sample/current",
		"/api/" + apiVersion + "/skills/team/sample/publications/" + digest + "/tree.zip",
	}
	for _, path := range readPaths {
		fixture.get(path)
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
		fixture.server.URL+"/api/"+apiVersion+"/skills/team/sample/publications/"+digest+"/tree.zip",
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
		headerPublicationNamespace: "team",
		headerPublicationName:      "sample",
		headerPublicationDigest:    digest,
	} {
		if got := response.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	ceiling := agentskill.TreeArchiveMaxBytes
	if int64(len(body)) > ceiling {
		t.Fatalf("tree ZIP bytes = %d, exceeds protocol ceiling %d", len(body), ceiling)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range archive.File {
		if file.Method != agentskill.TreeArchiveZIPMethod {
			t.Fatalf("tree ZIP entry %q method = %d, want %d", file.Name, file.Method, agentskill.TreeArchiveZIPMethod)
		}
		names = append(names, file.Name)
	}
	wantNames := []string{"SKILL.md", "assets/inert.txt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("tree ZIP entries = %v, want %v", names, wantNames)
	}
}

type cancelAfterReadTree struct {
	files  fstest.MapFS
	cancel context.CancelFunc
}

func (t cancelAfterReadTree) Open(name string) (fs.File, error) {
	file, err := t.files.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	return &cancelAfterReadTreeFile{File: file, cancel: t.cancel}, nil
}

func (t cancelAfterReadTree) ReadDir(name string) ([]fs.DirEntry, error) {
	return t.files.ReadDir(name)
}

type cancelAfterReadTreeFile struct {
	fs.File
	cancelled bool
	cancel    context.CancelFunc
}

func (t *cancelAfterReadTreeFile) Read(buffer []byte) (int, error) {
	read, err := t.File.Read(buffer)
	if read > 0 && !t.cancelled {
		t.cancelled = true
		t.cancel()
	}
	return read, err
}

func TestRootlessZIPHonorsCancellationWhileStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	staging := t.TempDir()
	h := &handler{options: handlerOptions{StagingParent: staging}, maxArchiveBytes: agentskill.TreeArchiveMaxBytes}
	tree := cancelAfterReadTree{
		files:  fstest.MapFS{"large": {Data: bytes.Repeat([]byte("x"), 128<<10)}},
		cancel: cancel,
	}
	if _, err := h.rootlessZIP(ctx, tree); !errors.Is(err, context.Canceled) {
		t.Fatalf("rootlessZIP() error = %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial archive remains after cancellation: %v", entries)
	}
}

func TestRootlessZIPFitsProtocolMetadataAllowance(t *testing.T) {
	ceiling := agentskill.TreeArchiveMaxBytes
	h := &handler{options: handlerOptions{StagingParent: t.TempDir()}, maxArchiveBytes: ceiling}
	archive, err := h.rootlessZIP(context.Background(), fstest.MapFS{
		"SKILL.md":        {Data: []byte("s")},
		"assets/data.txt": {Data: []byte("a")},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := archive.Name()
	info, err := archive.Stat()
	closeErr := archive.Close()
	removeErr := os.Remove(name)
	if err := errors.Join(err, closeErr, removeErr); err != nil {
		t.Fatal(err)
	}
	if info.Size() > ceiling {
		t.Fatalf("tree ZIP bytes = %d, exceeds protocol ceiling %d", info.Size(), ceiling)
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
	if !strings.Contains(detail, "/api/"+apiVersion+"/skills/team%3Fx/sample/publications/") {
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

func TestResolveTreeFileRespectsCancellation(t *testing.T) {
	tree := fstest.MapFS{
		"SKILL.md":        {Data: []byte("---\nname: sample\ndescription: Cancellation test\n---\nBody.\n")},
		"assets/data.txt": {Data: []byte("asset")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveTreeFile(ctx, tree, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveTreeFile with cancelled context error = %v", err)
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

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func failingTemplates(names ...string) *template.Template {
	funcs := template.FuncMap{
		"boom": func() (string, error) { return "", errors.New("template boom") },
	}
	var pages *template.Template
	for _, name := range names {
		if pages == nil {
			pages = template.Must(template.New(name).Funcs(funcs).Parse(`{{boom}}`))
			continue
		}
		pages = template.Must(pages.New(name).Funcs(funcs).Parse(`{{boom}}`))
	}
	return pages
}

func TestRenderFallsBackWhenPageTemplateFails(t *testing.T) {
	pages := failingTemplates("page")
	template.Must(pages.New("error").Parse(`<h1>{{.Title}}</h1><p>{{.Action}}</p>`))
	h := &handler{pages: pages, options: handlerOptions{Logger: discardLogger()}}

	recorder := httptest.NewRecorder()
	h.render(recorder, http.StatusOK, "page", nil)

	// The 200 must never commit: the response is the buffered 500 fallback.
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), "Page could not be rendered") {
		t.Fatalf("body = %q, want the generic error page", recorder.Body.String())
	}
}

func TestRenderErrorFallsBackToPlaintextWhenErrorTemplateFails(t *testing.T) {
	h := &handler{pages: failingTemplates("error"), options: handlerOptions{Logger: discardLogger()}}

	recorder := httptest.NewRecorder()
	h.renderError(recorder, http.StatusInternalServerError, "Unreachable", "Never rendered")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content type = %q, want the plaintext fallback", got)
	}
	if !strings.Contains(recorder.Body.String(), "Internal Server Error") {
		t.Fatalf("body = %q, want the plaintext status text", recorder.Body.String())
	}
}

func TestHandleErrorLogsUnexpectedFailure(t *testing.T) {
	pages, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	captured := &recordSlogHandler{}
	h := &handler{pages: pages, options: handlerOptions{Logger: slog.New(captured)}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/candidates", nil)
	h.handleError(recorder, request, errors.New("catalog storage unavailable"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if body := recorder.Body.String(); strings.Contains(body, "catalog storage unavailable") {
		t.Fatalf("response leaked the internal error: %q", body)
	}
	for _, want := range []string{"web request failed", "catalog storage unavailable", "POST", "/candidates"} {
		if !captured.contains(want) {
			t.Errorf("log has no record carrying %q", want)
		}
	}
}

func TestWriteAPIDomainErrorLogsUnexpectedFailure(t *testing.T) {
	captured := &recordSlogHandler{}
	h := &handler{options: handlerOptions{Logger: slog.New(captured)}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/"+apiVersion+"/skills/team/sample/current", nil)

	h.writeAPIDomainError(recorder, request, errors.New("catalog storage unavailable"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	for _, want := range []string{"API request failed", "catalog storage unavailable", "GET", request.URL.Path} {
		if !captured.contains(want) {
			t.Errorf("log has no record carrying %q", want)
		}
	}
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
	catalog, err := openCatalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.close() })
	resolver := &fixedCuratorResolver{}
	handler, err := newHandler(catalog, resolver.resolve, handlerOptions{
		StagingParent: t.TempDir(), Limits: safetree.PrototypeLimits(), CSRFKey: key, SecureCookies: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.close(); err != nil {
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
