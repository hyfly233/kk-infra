package auth

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Service tokens cannot be used as controlplane user sessions.
func IssueServiceToken(secret, caller, service string) (string, error) {
	if secret == "" || caller == "" || service == "" {
		return "", fmt.Errorf("service identity configuration required")
	}
	return IssueAccessToken(secret, caller, "", RoleViewer, "service:"+service, time.Now(), time.Minute)
}

func AuthenticateService(secret, token, service string, callers ...string) error {
	if secret == "" {
		return fmt.Errorf("service authentication disabled")
	}
	c, err := ParseAccessToken(secret, token, "service:"+service)
	if err != nil || c.Role != RoleViewer || c.TenantID != "" {
		return fmt.Errorf("invalid service identity")
	}
	for _, caller := range callers {
		if c.Subject == caller {
			return nil
		}
	}
	return fmt.Errorf("unauthorized service caller")
}

func BearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}
