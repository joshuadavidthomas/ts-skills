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

	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/storage"
)

type fixedActorResolver struct{ actor registry.Actor }

func (r fixedActorResolver) Actor(*http.Request) (registry.Actor, error) { return r.actor, nil }

type webFixture struct {
	t       *testing.T
	server  *httptest.Server
	client  *http.Client
	cookie  *http.Cookie
	token   string
	storage *storage.Catalog
	state   string
	staging string
	actor   registry.Actor
	key     CSRFKey
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
	handler, err := NewHandler(catalog, fixedActorResolver{actor: actor}, Options{
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
		state: state, staging: staging, actor: actor, key: key,
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
	handler, err := NewHandler(catalog, fixedActorResolver{actor: f.actor}, Options{
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

func skillZIP(t *testing.T, instructions string) []byte {
	t.Helper()
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	files := map[string]string{
		"SKILL.md":            "---\nname: sample\ndescription: Web test\n---\n" + instructions,
		"assets/<script>.txt": "inert asset",
	}
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
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

func (f *webFixture) uploadZIP(instructions string) string {
	f.t.Helper()
	request := multipartRequest(f.t, f.server.URL+"/candidates", []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "kind", body: []byte("zip")},
		{name: "archive", filename: "sample.zip", body: skillZIP(f.t, instructions)},
	})
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

func TestCurationRoutesEscapeReviewPublishAndChangeCurrent(t *testing.T) {
	fixture := newWebFixture(t)
	firstPath := fixture.uploadZIP("<script>globalThis.pwned = true</script>\n")
	firstReview := fixture.get(firstPath)
	if strings.Contains(firstReview, "<script>globalThis.pwned") {
		t.Fatal("imported SKILL.md rendered as active script")
	}
	for _, escaped := range []string{"&lt;script&gt;globalThis.pwned", "assets/&lt;script&gt;.txt", "Curator &lt;One&gt;"} {
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

	secondPath := fixture.uploadZIP("Second inert revision.\n")
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

func TestEquivalentZIPAndDirectoryUploadsHaveSameReviewDigest(t *testing.T) {
	fixture := newWebFixture(t)
	zipPath := fixture.uploadZIP("Equivalent.\n")
	zipDigest := digestPattern.FindString(fixture.get(zipPath))
	skill := "---\nname: sample\ndescription: Web test\n---\nEquivalent.\n"
	asset := "inert asset"
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d},{"index":1,"path":"sample/assets/<script>.txt","size":%d}]`, len(skill), len(asset))
	request := multipartRequest(t, fixture.server.URL+"/candidates", []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "kind", body: []byte("directory")},
		{name: "manifest", body: []byte(manifest)},
		{name: "file-0", filename: "SKILL.md", body: []byte(skill)},
		{name: "file-1", filename: "not-the-path.txt", body: []byte(asset)},
	})
	response := fixture.do(request, true)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("directory upload status = %d", response.StatusCode)
	}
	directoryDigest := digestPattern.FindString(fixture.get(response.Header.Get("Location")))
	if zipDigest != directoryDigest {
		t.Fatalf("ZIP digest %s != directory digest %s", zipDigest, directoryDigest)
	}
}

func TestPublishedCurationSurvivesStorageAndHandlerRestart(t *testing.T) {
	fixture := newWebFixture(t)
	candidatePath := fixture.uploadZIP("Persist across restart.\n")
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
	if !strings.Contains(restartedReview, "Persist across restart.") || !strings.Contains(restartedReview, "This candidate is published") {
		t.Fatalf("restarted review lost candidate facts: %s", restartedReview)
	}
}

func TestUploadLimitMapsToRequestEntityTooLarge(t *testing.T) {
	fixture := newWebFixture(t)
	manifest := fmt.Sprintf(`[{"index":0,"path":"sample/SKILL.md","size":%d}]`, (16<<20)+1)
	request := multipartRequest(t, fixture.server.URL+"/candidates", []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "kind", body: []byte("directory")},
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
	validParts := []formPart{
		{name: "namespace", body: []byte("team")},
		{name: "kind", body: []byte("zip")},
		{name: "archive", filename: "sample.zip", body: skillZIP(t, "Safe.\n")},
	}
	response := fixture.do(multipartRequest(t, fixture.server.URL+"/candidates", validParts), false)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", response.StatusCode)
	}

	reordered := []formPart{validParts[1], validParts[0], validParts[2]}
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
