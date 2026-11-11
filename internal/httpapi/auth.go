package httpapi

import (
	"crypto/sha256"
	"net/http"
	"strings"
)

// tokenScope is the set of accounts a presented account token can access.
type tokenScope struct {
	accounts map[string]struct{}
}

func (s tokenScope) allows(account string) bool {
	_, ok := s.accounts[account]
	return ok
}

// requestToken reads an account token from the Authorization header or the
// OneBot 11 access_token query parameter. The header wins when both exist.
func requestToken(r *http.Request) (string, bool) {
	if token, ok := bearerToken(r.Header.Get("Authorization")); ok {
		return token, true
	}
	if token := strings.TrimSpace(r.URL.Query().Get("access_token")); token != "" {
		return token, true
	}
	return "", false
}

func (s *Server) resolveToken(token string) (tokenScope, bool) {
	digest := sha256.Sum256([]byte(token))
	accounts, ok := s.accountTokens[digest]
	if !ok {
		return tokenScope{}, false
	}
	return tokenScope{accounts: accounts}, true
}

// authenticate resolves the request token. present is false when no token was
// supplied; valid is false when the token does not belong to any account.
func (s *Server) authenticate(r *http.Request) (scope tokenScope, present, valid bool) {
	token, present := requestToken(r)
	if !present {
		return tokenScope{}, false, false
	}
	scope, valid = s.resolveToken(token)
	return scope, true, valid
}

// authorizeAccount checks that the caller's token belongs to the given
// account. Account tokens are the only accepted credentials.
func (s *Server) authorizeAccount(w http.ResponseWriter, r *http.Request, account Account) bool {
	scope, present, valid := s.authenticate(r)
	if !present {
		w.Header().Set("WWW-Authenticate", `Bearer realm="message-gateway"`)
		writeOneBotError(w, http.StatusUnauthorized, 1403, "unauthorized")
		return false
	}
	if !valid {
		writeOneBotError(w, http.StatusForbidden, 1403, "forbidden")
		return false
	}
	if scope.allows(account.Name) {
		return true
	}
	writeOneBotError(w, http.StatusForbidden, 1403, "forbidden")
	return false
}

// requireLegacyToken authenticates a legacy endpoint and writes the legacy
// error shape. The handler must still check that the resolved account is in
// the returned scope.
func (s *Server) requireLegacyToken(w http.ResponseWriter, r *http.Request) (tokenScope, bool) {
	scope, present, valid := s.authenticate(r)
	if !present {
		w.Header().Set("WWW-Authenticate", `Bearer realm="message-gateway"`)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return tokenScope{}, false
	}
	if !valid {
		writeError(w, http.StatusForbidden, "forbidden")
		return tokenScope{}, false
	}
	return scope, true
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], true
}
