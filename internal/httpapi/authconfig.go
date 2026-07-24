package httpapi

import "net/http"

// authConfigResponse tells the browser console how to authenticate against this
// deployment. It is deliberately public: a console that cannot authenticate
// cannot ask an authenticated endpoint how to authenticate.
//
// It carries no secret. Issuer, audience and client id are public OIDC
// parameters that appear in the browser's address bar during the redirect.
type authConfigResponse struct {
	AuthEnabled bool   `json:"auth_enabled"`
	Issuer      string `json:"issuer,omitempty"`
	Audience    string `json:"audience,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
}

// serveAuthConfig publishes the console's authentication parameters.
func (s *Server) serveAuthConfig(w http.ResponseWriter, _ *http.Request) {
	resp := authConfigResponse{AuthEnabled: s.cfg.AuthEnabled}

	// Only meaningful when auth is on; omitted entirely in local development so
	// the console does not attempt a redirect it does not need.
	if s.cfg.AuthEnabled {
		resp.Issuer = s.cfg.JWTIssuer
		resp.Audience = s.cfg.JWTAudience
		resp.ClientID = s.cfg.OIDCClientID
	}

	writeJSON(w, http.StatusOK, resp)
}
