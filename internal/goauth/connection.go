// Package goauth holds the minimal Go-subscription connection state used by
// the gateway configuration. The full browser Device OAuth flow lives in the
// admin package; this package only defines the persisted connection shape and
// its validation rules.
package goauth

import (
	"errors"
	"fmt"
	"strings"
)

// Route describes how a single model is served through a Go subscription.
//
// Organization selects the OpenCode organization that owns the subscription,
// and Model is the upstream model identifier the route serves.
type Route struct {
	Organization string `json:"organization,omitempty"`
	Model        string `json:"model,omitempty"`
}

// Connection is the persisted state of a signed-in Go subscription. It holds
// only what the gateway needs to refresh and use the subscription; secrets are
// stored server-side and never echoed back through the management API or logs.
type Connection struct {
	// Proxy optionally pins the subscription to a configured proxy entry.
	Proxy string `json:"proxy,omitempty"`
	// Routes maps a model ID to the organization and model that serve it.
	Routes map[string]Route `json:"routes,omitempty"`
	// AccessToken and RefreshToken are the OAuth credentials. They are
	// redacted in every log and API response.
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// Validate reports whether the connection is complete enough to route.
func (c Connection) Validate() error {
	if c.AccessToken == "" && c.RefreshToken == "" {
		return errors.New("go_subscription requires an access or refresh token")
	}
	for model, route := range c.Routes {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("go_subscription.routes contains an empty model key")
		}
		if strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("go_subscription.routes[%q] has no model", model)
		}
	}
	return nil
}
