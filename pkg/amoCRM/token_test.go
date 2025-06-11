package amocrm_test

import (
	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	accessToken  = "access_token"
	refreshToken = "refresh_token"
	tokenType    = "bearer"
	expiresAt    = time.Now()
)

func TestNewToken(t *testing.T) {
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, expiresAt)
	require.Implements(t, (*amocrm.Token)(nil), token)
}

func TestTokenSource_AccessToken(t *testing.T) {
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, expiresAt)
	require.Exactly(t, accessToken, token.AccessToken())
}

func TestTokenSource_RefreshToken(t *testing.T) {
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, expiresAt)
	require.Exactly(t, refreshToken, token.RefreshToken())
}

func TestTokenSource_TokenType(t *testing.T) {
	cases := []struct {
		typeCode  string
		typeValue string
	}{
		{typeCode: "bearer", typeValue: "Bearer"},
		{typeCode: "mac", typeValue: "MAC"},
		{typeCode: "basic", typeValue: "Basic"},
		{typeCode: "example", typeValue: "example"},
	}
	for _, tc := range cases {
		token := amocrm.NewToken(accessToken, refreshToken, tc.typeCode, expiresAt)
		require.Exactly(t, tc.typeValue, token.TokenType())
	}
}

func TestTokenSource_ExpiresAt(t *testing.T) {
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, expiresAt)
	require.Exactly(t, expiresAt, token.ExpiresAt())
}

func TestTokenSource_Expired(t *testing.T) {
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, expiresAt)
	require.True(t, token.Expired())
}

func TestTokenSource_Expired_Limitless(t *testing.T) {
	token := amocrm.NewToken(accessToken, refreshToken, tokenType, time.Time{})
	require.False(t, token.Expired())
}

func TestTokenSource_Expired_EmptyAccessToken(t *testing.T) {
	token := amocrm.NewToken("", refreshToken, tokenType, expiresAt)
	require.True(t, token.Expired())
}
