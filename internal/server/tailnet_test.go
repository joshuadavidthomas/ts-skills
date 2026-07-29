package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func localClientForWhoIs(t *testing.T, response *apitype.WhoIsResponse, addresses *[]string) *local.Client {
	t.Helper()
	return &local.Client{
		OmitAuth: true,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/localapi/v0/whois" {
				t.Fatalf("LocalAPI path = %q, want /localapi/v0/whois", request.URL.Path)
			}
			*addresses = append(*addresses, request.URL.Query().Get("addr"))
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Request:    request,
			}, nil
		}),
	}
}

func TestNewTSNetServerCopiesConfigurationAndOwnsLogs(t *testing.T) {
	var messages []string
	logf := func(format string, _ ...any) { messages = append(messages, format) }
	tags := []string{"tag:skills-registry"}
	server := newTSNetServer(tailnetConfig{
		Hostname:      "registry",
		StateDir:      "/state",
		AuthKey:       "tskey-auth-test",
		AdvertiseTags: tags,
		Logf:          logf,
	})
	tags[0] = "tag:changed"

	if server.Hostname != "registry" || server.Dir != "/state" || server.AuthKey != "tskey-auth-test" {
		t.Fatalf("server fields = (%q, %q, %q)", server.Hostname, server.Dir, server.AuthKey)
	}
	if got := server.AdvertiseTags; len(got) != 1 || got[0] != "tag:skills-registry" {
		t.Fatalf("advertise tags = %v", got)
	}
	server.Logf("backend diagnostic")
	server.UserLogf("node status")
	if got := strings.Join(messages, ", "); got != "backend diagnostic, node status" {
		t.Fatalf("diagnostics = %q", got)
	}

	discard := newTSNetServer(tailnetConfig{Hostname: "registry", StateDir: "/state"})
	if discard.Logf == nil || discard.UserLogf == nil {
		t.Fatal("nil diagnostics were passed through to tsnet defaults")
	}
	discard.Logf("discarded backend diagnostic")
	discard.UserLogf("discarded node status")
}

func TestActorResolverUsesRemoteAddrAndIgnoresIdentityHeaders(t *testing.T) {
	var addresses []string
	client := localClientForWhoIs(t, &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "node-human", Name: "workstation.example.ts.net."},
		UserProfile: &tailcfg.UserProfile{ID: 42, LoginName: "alice@example.com"},
	}, &addresses)
	resolver, err := newActorResolver(client)
	if err != nil {
		t.Fatal(err)
	}

	for _, headers := range []http.Header{
		{"X-Forwarded-For": {"100.64.0.99"}, "X-Tailscale-User-Login": {"mallory@example.com"}},
		{"Forwarded": {"for=100.64.0.100"}, "X-Remote-User": {"mallory"}},
	} {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://registry.example.ts.net/candidates", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.RemoteAddr = "100.64.0.7:51820"
		request.Header = headers
		_, err = resolver.curator(request)
		if !errors.Is(err, errCurationDenied) {
			t.Fatalf("curator error = %v, want permission denial", err)
		}
	}

	if len(addresses) != 2 {
		t.Fatalf("WhoIs calls = %d, want 2", len(addresses))
	}
	for _, address := range addresses {
		if address != "100.64.0.7:51820" {
			t.Fatalf("WhoIs address = %q, want unmodified RemoteAddr", address)
		}
	}
}

func TestValidateActorRejectsUntrustedText(t *testing.T) {
	for _, actor := range []actor{
		{ID: "id\x00", Display: "display"},
		{ID: string([]byte{0xff}), Display: "display"},
		{ID: strings.Repeat("a", 257), Display: "display"},
	} {
		if err := validateActor(actor); err == nil {
			t.Fatalf("validateActor(%#v) succeeded", actor)
		}
	}
}

func TestActorResolverUsesTaggedNodeIdentity(t *testing.T) {
	var addresses []string
	client := localClientForWhoIs(t, &apitype.WhoIsResponse{
		Node: &tailcfg.Node{
			StableID: "n123CNTRL",
			Name:     "automation.example.ts.net.",
			Tags:     []string{"tag:ci", "tag:publisher"},
		},
		UserProfile: &tailcfg.UserProfile{ID: 42, LoginName: "tag-owner@example.com"},
		CapMap: tailcfg.PeerCapMap{
			skillsCapabilityName: {tailcfg.RawMessage(`{"curate":true}`)},
		},
	}, &addresses)
	resolver, err := newActorResolver(client)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://registry.example.ts.net/candidates", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.RemoteAddr = "[fd7a:115c:a1e0::7]:44321"

	curator, err := resolver.curator(request)
	if err != nil {
		t.Fatal(err)
	}
	if curator.Actor.ID != "n123CNTRL" {
		t.Fatalf("actor ID = %q, want stable node ID", curator.Actor.ID)
	}
	if curator.Actor.Display != "automation.example.ts.net [tag:ci, tag:publisher]" {
		t.Fatalf("actor display = %q", curator.Actor.Display)
	}
	if len(addresses) != 1 || addresses[0] != request.RemoteAddr {
		t.Fatalf("WhoIs addresses = %v, want %q", addresses, request.RemoteAddr)
	}
}

func TestActorResolverCapabilityRules(t *testing.T) {
	tests := map[string]struct {
		capMap     tailcfg.PeerCapMap
		wantCurate bool
		wantError  bool
	}{
		"granted by any rule": {
			capMap: tailcfg.PeerCapMap{
				skillsCapabilityName: {
					tailcfg.RawMessage(`{"curate":false}`),
					tailcfg.RawMessage(`{"curate":true}`),
				},
			},
			wantCurate: true,
		},
		"absent": {},
		"malformed rule": {
			capMap: tailcfg.PeerCapMap{
				skillsCapabilityName: {tailcfg.RawMessage(`{"curate":"yes"}`)},
			},
			wantError: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var addresses []string
			client := localClientForWhoIs(t, &apitype.WhoIsResponse{
				Node:        &tailcfg.Node{StableID: "node-human", Name: "workstation.example.ts.net."},
				UserProfile: &tailcfg.UserProfile{ID: 42, LoginName: "alice@example.com"},
				CapMap:      test.capMap,
			}, &addresses)
			resolver, err := newActorResolver(client)
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://registry.example.ts.net/candidates", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.RemoteAddr = "100.64.0.7:51820"

			_, err = resolver.curator(request)
			if test.wantError {
				if err == nil {
					t.Fatal("Curator succeeded with malformed capability rule")
				}
				return
			}
			if test.wantCurate {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, errCurationDenied) {
				t.Fatalf("curator error = %v, want permission denial", err)
			}
		})
	}
}

func TestActorResolverValidatesIdentityBeforeCapability(t *testing.T) {
	var addresses []string
	resolver, err := newActorResolver(localClientForWhoIs(t, &apitype.WhoIsResponse{
		Node: &tailcfg.Node{StableID: "node-human", Name: "human.example.ts.net."},
	}, &addresses))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://registry.example.ts.net/candidates", nil)
	request.RemoteAddr = "100.64.0.7:51820"
	if _, err := resolver.curator(request); err == nil || errors.Is(err, errCurationDenied) {
		t.Fatalf("curator error = %v, want incomplete identity failure", err)
	}
}

func TestActorResolverRejectsIncompleteWhoIsIdentity(t *testing.T) {
	tests := map[string]*apitype.WhoIsResponse{
		"missing node": {CapMap: tailcfg.PeerCapMap{skillsCapabilityName: {tailcfg.RawMessage(`{"curate":true}`)}}},
		"missing human profile": {
			Node:   &tailcfg.Node{StableID: "node-human", Name: "human.example.ts.net."},
			CapMap: tailcfg.PeerCapMap{skillsCapabilityName: {tailcfg.RawMessage(`{"curate":true}`)}},
		},
		"tagged node without stable ID": {
			Node:   &tailcfg.Node{Name: "automation.example.ts.net.", Tags: []string{"tag:ci"}},
			CapMap: tailcfg.PeerCapMap{skillsCapabilityName: {tailcfg.RawMessage(`{"curate":true}`)}},
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			var addresses []string
			resolver, err := newActorResolver(localClientForWhoIs(t, response, &addresses))
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://registry.example.ts.net/", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.RemoteAddr = "100.64.0.8:1234"
			if _, err := resolver.curator(request); err == nil {
				t.Fatal("Curator succeeded with incomplete WhoIs identity")
			}
		})
	}
}
