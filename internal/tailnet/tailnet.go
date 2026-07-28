package tailnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

const startupTimeout = 2 * time.Minute

// ActorResolver maps the peer accepted by tsnet to a registry actor. It does
// not consult HTTP headers because they are controlled by the caller.
type ActorResolver struct {
	local *local.Client
}

func NewActorResolver(client *local.Client) (*ActorResolver, error) {
	if client == nil {
		return nil, fmt.Errorf("Tailnet LocalAPI client must be provided")
	}
	return &ActorResolver{local: client}, nil
}

func (r *ActorResolver) Actor(request *http.Request) (registry.Actor, error) {
	if r == nil || r.local == nil {
		return registry.Actor{}, fmt.Errorf("resolve Tailnet actor: LocalAPI client is unavailable")
	}
	if request == nil {
		return registry.Actor{}, fmt.Errorf("resolve Tailnet actor: HTTP request must be provided")
	}

	who, err := r.local.WhoIs(request.Context(), request.RemoteAddr)
	if err != nil {
		return registry.Actor{}, fmt.Errorf("identify Tailnet peer %q: %w", request.RemoteAddr, err)
	}
	if who == nil || who.Node == nil {
		return registry.Actor{}, fmt.Errorf("identify Tailnet peer %q: WhoIs returned no node", request.RemoteAddr)
	}

	if len(who.Node.Tags) != 0 {
		if who.Node.StableID.IsZero() || strings.TrimSpace(who.Node.Name) == "" {
			return registry.Actor{}, fmt.Errorf("identify tagged Tailnet peer %q: node identity is incomplete", request.RemoteAddr)
		}
		display := strings.TrimSuffix(who.Node.Name, ".") + " [" + strings.Join(who.Node.Tags, ", ") + "]"
		actor, err := registry.NewActor(string(who.Node.StableID), display)
		if err != nil {
			return registry.Actor{}, fmt.Errorf("identify tagged Tailnet peer %q: %w", request.RemoteAddr, err)
		}
		return actor, nil
	}

	if who.UserProfile == nil || who.UserProfile.ID.IsZero() || strings.TrimSpace(who.UserProfile.LoginName) == "" {
		return registry.Actor{}, fmt.Errorf("identify human Tailnet peer %q: user identity is incomplete", request.RemoteAddr)
	}
	actor, err := registry.NewActor(
		strconv.FormatInt(int64(who.UserProfile.ID), 10),
		who.UserProfile.LoginName,
	)
	if err != nil {
		return registry.Actor{}, fmt.Errorf("identify human Tailnet peer %q: %w", request.RemoteAddr, err)
	}
	return actor, nil
}

type ServerConfig struct {
	Hostname      string
	StateDir      string
	AuthKey       string
	AdvertiseTags []string
}

// Server owns one embedded Tailscale node and its Tailnet-only TLS listener.
type Server struct {
	server   *tsnet.Server
	listener net.Listener
}

func ListenTLS(ctx context.Context, config ServerConfig) (_ *Server, err error) {
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

	ts := &tsnet.Server{
		Hostname:      config.Hostname,
		Dir:           config.StateDir,
		AuthKey:       config.AuthKey,
		AdvertiseTags: append([]string(nil), config.AdvertiseTags...),
	}
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
	return &Server{server: ts, listener: listener}, nil
}

func (s *Server) Listener() net.Listener {
	if s == nil {
		return nil
	}
	return s.listener
}

func (s *Server) LocalClient() (*local.Client, error) {
	if s == nil || s.server == nil {
		return nil, fmt.Errorf("Tailnet server is unavailable")
	}
	client, err := s.server.LocalClient()
	if err != nil {
		return nil, fmt.Errorf("open embedded Tailscale LocalAPI: %w", err)
	}
	return client, nil
}

func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Close()
}
