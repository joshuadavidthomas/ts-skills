package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	servercatalog "github.com/joshuadavidthomas/ts-skills/internal/server/catalog"
	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
)

const (
	startupTimeout       = 2 * time.Minute
	skillsCapabilityName = tailcfg.PeerCapability("joshuadavidthomas.com/cap/ts-skills")
)

type capabilityRule struct {
	Curate bool `json:"curate"`
}

// actorResolver maps the peer accepted by tsnet to its registry identity. It
// does not consult HTTP headers because they are controlled by the caller.
type actorResolver struct {
	local *local.Client
}

func (r *actorResolver) curator(request *http.Request) (servercatalog.Curator, error) {
	if r == nil || r.local == nil {
		return servercatalog.Curator{}, fmt.Errorf("resolve Tailnet identity: LocalAPI client is unavailable")
	}
	if request == nil {
		return servercatalog.Curator{}, fmt.Errorf("resolve Tailnet identity: HTTP request must be provided")
	}

	who, err := r.local.WhoIs(request.Context(), request.RemoteAddr)
	if err != nil {
		return servercatalog.Curator{}, fmt.Errorf("identify Tailnet peer %q: %w", request.RemoteAddr, err)
	}
	if who == nil || who.Node == nil {
		return servercatalog.Curator{}, fmt.Errorf("identify Tailnet peer %q: WhoIs returned no node", request.RemoteAddr)
	}
	var resolvedActor servercatalog.Actor
	if len(who.Node.Tags) != 0 {
		if who.Node.StableID.IsZero() || strings.TrimSpace(who.Node.Name) == "" {
			return servercatalog.Curator{}, fmt.Errorf("identify tagged Tailnet peer %q: node identity is incomplete", request.RemoteAddr)
		}
		display := strings.TrimSuffix(who.Node.Name, ".") + " [" + strings.Join(who.Node.Tags, ", ") + "]"
		resolvedActor, err = servercatalog.NewActor(string(who.Node.StableID), display)
		if err != nil {
			return servercatalog.Curator{}, fmt.Errorf("identify tagged Tailnet peer %q: %w", request.RemoteAddr, err)
		}
	} else {
		if who.UserProfile == nil || who.UserProfile.ID.IsZero() || strings.TrimSpace(who.UserProfile.LoginName) == "" {
			return servercatalog.Curator{}, fmt.Errorf("identify human Tailnet peer %q: user identity is incomplete", request.RemoteAddr)
		}
		resolvedActor, err = servercatalog.NewActor(strconv.FormatInt(int64(who.UserProfile.ID), 10), who.UserProfile.LoginName)
		if err != nil {
			return servercatalog.Curator{}, fmt.Errorf("identify human Tailnet peer %q: %w", request.RemoteAddr, err)
		}
	}

	rules, err := tailcfg.UnmarshalCapJSON[capabilityRule](who.CapMap, skillsCapabilityName)
	if err != nil {
		return servercatalog.Curator{}, fmt.Errorf("identify Tailnet peer %q capabilities: %w", request.RemoteAddr, err)
	}
	for _, rule := range rules {
		if rule.Curate {
			return servercatalog.Curator{Actor: resolvedActor}, nil
		}
	}
	return servercatalog.Curator{}, fmt.Errorf("identify Tailnet peer %q: %w", request.RemoteAddr, servercatalog.ErrCurationDenied)
}

type tailnetConfig struct {
	Hostname      string
	StateDir      string
	AuthKey       string
	AdvertiseTags []string
	// Logf receives both verbose backend diagnostics and user-facing tsnet
	// status. A nil value suppresses both kinds of output.
	Logf func(string, ...any)
}

// tailnetServer owns one embedded Tailscale node and its Tailnet-only TLS listener.
type tailnetServer struct {
	server   *tsnet.Server
	listener net.Listener
}

func listenTLS(ctx context.Context, config tailnetConfig) (_ *tailnetServer, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("start Tailnet server: context must be provided")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Hostname) == "" {
		return nil, fmt.Errorf("start Tailnet server: hostname must be provided")
	}
	if strings.TrimSpace(config.StateDir) == "" {
		return nil, fmt.Errorf("start Tailnet server: state directory must be provided")
	}

	ts := newTSNetServer(config)
	if err := ts.Start(); err != nil {
		return nil, fmt.Errorf("start embedded Tailscale node: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			err = errors.Join(err, ts.Close())
		}
	}()

	upCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	_, upErr := ts.Up(upCtx)
	cancel()
	if upErr != nil {
		return nil, fmt.Errorf("wait for embedded Tailscale node: %w", upErr)
	}

	listener, listenErr := ts.ListenTLS("tcp", ":443")
	if listenErr != nil {
		return nil, fmt.Errorf("listen with Tailnet HTTPS: %w", listenErr)
	}
	closeOnError = false
	return &tailnetServer{server: ts, listener: listener}, nil
}

func newTSNetServer(config tailnetConfig) *tsnet.Server {
	logf := config.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &tsnet.Server{
		Hostname:      config.Hostname,
		Dir:           config.StateDir,
		AuthKey:       config.AuthKey,
		AdvertiseTags: append([]string(nil), config.AdvertiseTags...),
		Logf:          logf,
		UserLogf:      logf,
	}
}

func (s *tailnetServer) listenerAddr() net.Listener {
	if s == nil {
		return nil
	}
	return s.listener
}

func (s *tailnetServer) localClient() (*local.Client, error) {
	if s == nil || s.server == nil {
		return nil, fmt.Errorf("tailnet server is unavailable")
	}
	client, err := s.server.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("open embedded Tailscale LocalAPI: %w", err)
	}
	return client, nil
}

func (s *tailnetServer) close() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Close()
}
