package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestServiceIdentityIsCallerAndAudienceBound(t *testing.T) {
	token, err := IssueServiceToken("secret", "controlplane", "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthenticateService("secret", token, "gateway", "controlplane"); err != nil {
		t.Fatal(err)
	}
	if AuthenticateService("secret", token, "observability", "controlplane") == nil {
		t.Fatal("wrong audience accepted")
	}
	if AuthenticateService("secret", token, "gateway", "pipeline") == nil {
		t.Fatal("wrong caller accepted")
	}
	if _, err := ParseAccessToken("secret", token, "controlplane"); err == nil {
		t.Fatal("service became user")
	}
	user, _ := IssueAccessToken("secret", "controlplane", "tenant-a", RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	if AuthenticateService("secret", user, "gateway", "controlplane") == nil {
		t.Fatal("user became service")
	}
}

func TestJWTRequiresExpiryAndKnownRole(t *testing.T) {
	for _, claims := range []Claims{
		{Role: RoleViewer, RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Audience: jwt.ClaimStrings{"controlplane"}}},
		{Role: Role("superuser"), RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Audience: jwt.ClaimStrings{"controlplane"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}},
		{Role: RoleViewer, RegisteredClaims: jwt.RegisteredClaims{Subject: "u", Audience: jwt.ClaimStrings{"controlplane"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}},
	} {
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseAccessToken("secret", token, "controlplane"); err == nil {
			t.Fatal("invalid claims accepted")
		}
	}
}
