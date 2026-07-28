package web

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
	"github.com/joshuadavidthomas/ts-skills/internal/storage"
)

type fixedActorResolver struct{ identity Identity }

func (r *fixedActorResolver) Identify(*http.Request) (Identity, error) { return r.identity, nil }

type webFixture struct {
	t        *testing.T
	server   *httptest.Server
	client   *http.Client
	cookie   *http.Cookie
	token    string
	storage  *storage.Catalog
	state    string
	staging  string
	resolver *fixedActorResolver
	key      CSRFKey
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	state := t.TempDir()
	records, err := storage.OpenCatalog(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	catalog, err := registry.NewCatalog(records, staging, safetree.PrototypeLimits())
	if err != nil {
		t.Fatal(err)
	}
	actor, err := registry.NewActor("user:42", "Curator <One>")
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := bytes.Repeat([]byte{0x5a}, 32)
	key, err := NewCSRFKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedActorResolver{identity: Identity{Actor: actor, CanCurate: true}}
	handler, err := NewHandler(catalog, resolver, Options{
		StagingParent: staging, Limits: safetree.PrototypeLimits(), CSRFKey: key, SecureCookies: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
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
		state: state, staging: staging, resolver: resolver, key: key,
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
	records, err := storage.OpenCatalog(context.Background(), f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	catalog, err := registry.NewCatalog(records, f.staging, safetree.PrototypeLimits())
	if err != nil {
		f.t.Fatal(err)
	}
	handler, err := NewHandler(catalog, f.resolver, Options{
		StagingParent: f.staging, Limits: safetree.PrototypeLimits(), CSRFKey: f.key, SecureCookies: false,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.storage = records
	f.server = httptest.NewServer(handler)
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
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d},{"index":1,"path":"sample/assets/<script>.txt","size":%d}]`, len(skill), len(asset))
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
	defer response.Body.Close()
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
	defer response.Body.Close()
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
	for _, escaped := range []string{"&lt;script&gt;globalThis.pwned", `href="?file=assets/%3Cscript%3E.txt"`, "Curator &lt;One&gt;"} {
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

	fixture.resolver.identity.CanCurate = false
	readPaths := []string{
		"/",
		unpublishedPath,
		"/api/" + protocol.Version + "/skills/team/sample/current",
		"/api/" + protocol.Version + "/skills/team/sample/publications/" + digest + "/tree.zip",
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
				!strings.Contains(string(body), "tailscale.com/cap/ts-skills") {
				t.Fatalf("permission error is missing guidance: %s", body)
			}
		})
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
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	wantNames := []string{"SKILL.md", "assets/<script>.txt"}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("tree ZIP entries = %v, want %v", names, wantNames)
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
			!strings.Contains(defaultPage, `href="?file=assets/%3Cscript%3E.txt"`) {
			t.Fatalf("page does not show the nested asset link: %s", defaultPage)
		}

		selectedPath := pagePath + "?file=" + url.QueryEscape("assets/<script>.txt")
		selectedPage := fixture.get(selectedPath)
		if !strings.Contains(selectedPage, "inert asset") || !strings.Contains(selectedPage, `>assets/&lt;script&gt;.txt</h3>`) {
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

func TestUploadRequiresCSRFAndExactMultipartOrder(t *testing.T) {
	fixture := newWebFixture(t)
	validParts := skillDirectoryParts("Safe.\n")
	response := fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", validParts), false)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", response.StatusCode)
	}

	reordered := append([]formPart{validParts[1], validParts[0]}, validParts[2:]...)
	response = fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", reordered), true)
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "Upload is invalid") {
		t.Fatalf("reordered status/body = %d/%s", response.StatusCode, body)
	}

	extra := append(append([]formPart{}, validParts...), formPart{name: "extra", body: []byte("x")})
	response = fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", extra), true)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("extra part status = %d", response.StatusCode)
	}
}

func TestNewHandlerValidatesCSRFAndOptions(t *testing.T) {
	if _, err := NewCSRFKey(make([]byte, 31)); err == nil {
		t.Fatal("short CSRF key accepted")
	}
	if _, err := NewCSRFKey(make([]byte, 32)); err == nil {
		t.Fatal("zero CSRF key accepted")
	}
	key, err := NewCSRFKey(bytes.Repeat([]byte{1}, 32))
	if err != nil || key == (CSRFKey{}) {
		t.Fatalf("valid key = %x, %v", key, err)
	}
}
