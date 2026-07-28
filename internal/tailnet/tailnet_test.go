package tailnet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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

func TestActorResolverUsesRemoteAddrAndIgnoresIdentityHeaders(t *testing.T) {
	var addresses []string
	client := localClientForWhoIs(t, &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "node-human", Name: "workstation.example.ts.net."},
		UserProfile: &tailcfg.UserProfile{ID: 42, LoginName: "alice@example.com"},
	}, &addresses)
	resolver, err := NewActorResolver(client)
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
		actor, err := resolver.Actor(request)
		if err != nil {
			t.Fatal(err)
		}
		if actor.ID() != "42" || actor.Display() != "alice@example.com" {
			t.Fatalf("actor = (%q, %q), want stable user identity", actor.ID(), actor.Display())
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

func TestActorResolverUsesTaggedNodeIdentity(t *testing.T) {
	var addresses []string
	client := localClientForWhoIs(t, &apitype.WhoIsResponse{
		Node: &tailcfg.Node{
			StableID: "n123CNTRL",
			Name:     "automation.example.ts.net.",
			Tags:     []string{"tag:ci", "tag:publisher"},
		},
		UserProfile: &tailcfg.UserProfile{ID: 42, LoginName: "tag-owner@example.com"},
	}, &addresses)
	resolver, err := NewActorResolver(client)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://registry.example.ts.net/candidates", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.RemoteAddr = "[fd7a:115c:a1e0::7]:44321"

	actor, err := resolver.Actor(request)
	if err != nil {
		t.Fatal(err)
	}
	if actor.ID() != "n123CNTRL" {
		t.Fatalf("actor ID = %q, want stable node ID", actor.ID())
	}
	if actor.Display() != "automation.example.ts.net [tag:ci, tag:publisher]" {
		t.Fatalf("actor display = %q", actor.Display())
	}
	if len(addresses) != 1 || addresses[0] != request.RemoteAddr {
		t.Fatalf("WhoIs addresses = %v, want %q", addresses, request.RemoteAddr)
	}
}

func TestActorResolverRejectsIncompleteWhoIsIdentity(t *testing.T) {
	tests := map[string]*apitype.WhoIsResponse{
		"missing node": {},
		"missing human profile": {
			Node: &tailcfg.Node{StableID: "node-human", Name: "human.example.ts.net."},
		},
		"tagged node without stable ID": {
			Node: &tailcfg.Node{Name: "automation.example.ts.net.", Tags: []string{"tag:ci"}},
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			var addresses []string
			resolver, err := NewActorResolver(localClientForWhoIs(t, response, &addresses))
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://registry.example.ts.net/", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.RemoteAddr = "100.64.0.8:1234"
			if _, err := resolver.Actor(request); err == nil {
				t.Fatal("Actor succeeded with incomplete WhoIs identity")
			}
		})
	}
}
