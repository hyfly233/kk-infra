package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Role 是固定四级 RBAC 角色。
type Role string

const (
	RolePlatformAdmin Role = "platform_admin"
	RoleTenantAdmin   Role = "tenant_admin"
	RoleDeveloper     Role = "developer"
	RoleViewer        Role = "viewer"
)

// Claims 是 access token 载荷；TenantID 为空只允许平台管理员。
type Claims struct {
	TenantID string `json:"tenantId,omitempty"`
	Role     Role   `json:"role"`
	jwt.RegisteredClaims
}

// IssueAccessToken 签发短期 HS256 access token。
func IssueAccessToken(secret, userID, tenantID string, role Role, audience string, now time.Time, ttl time.Duration) (string, error) {
	claims := Claims{TenantID: tenantID, Role: role, RegisteredClaims: jwt.RegisteredClaims{
		Subject: userID, Audience: jwt.ClaimStrings{audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseAccessToken 验证签名、有效期与预期 audience。
func ParseAccessToken(secret, tokenString, audience string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	}, jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid access token")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || claims.Subject == "" || !ValidRole(claims.Role) || (audience != "" && !hasAudience(claims.Audience, audience)) {
		return nil, fmt.Errorf("invalid access token claims")
	}
	return claims, nil
}

func ValidRole(role Role) bool {
	return role == RolePlatformAdmin || role == RoleTenantAdmin || role == RoleDeveloper || role == RoleViewer
}

func hasAudience(audiences jwt.ClaimStrings, wanted string) bool {
	for _, audience := range audiences {
		if audience == wanted {
			return true
		}
	}
	return false
}

// NewRefreshToken 返回仅供一次会话存储的高熵明文 token 与其 SHA-256 哈希。
func NewRefreshToken() (plain, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashRefreshToken(plain), nil
}

func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
