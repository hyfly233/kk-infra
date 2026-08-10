package auth

import (
	"testing"
	"time"
)

func TestAccessTokenAudienceAndExpiry(t *testing.T) {
	now := time.Now()
	token, err := IssueAccessToken("secret", "u1", "t1", RoleDeveloper, "controlplane", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseAccessToken("secret", token, "controlplane")
	if err != nil || claims.Subject != "u1" || claims.TenantID != "t1" {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	if _, err := ParseAccessToken("secret", token, "gateway"); err == nil {
		t.Fatal("wrong audience accepted")
	}
}

func TestRefreshTokenIsHashed(t *testing.T) {
	plain, hash, err := NewRefreshToken()
	if err != nil || plain == hash || HashRefreshToken(plain) != hash {
		t.Fatalf("refresh token: %q %q %v", plain, hash, err)
	}
}
