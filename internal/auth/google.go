package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

const googleAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
const googleTokenEndpoint = "https://oauth2.googleapis.com/token"

type GoogleProvider interface {
	AuthorizationURL(oauthState, nonce, codeVerifier string) string
	VerifyAuthorization(context.Context, string, string, string) (GoogleIdentity, error)
}

type GoogleOIDCProvider struct {
	config          oauth2.Config
	validateIDToken func(context.Context, string, string) (*idtoken.Payload, error)
}

func NewGoogleOIDCProvider(clientID, clientSecret, callbackURL string) (*GoogleOIDCProvider, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, errors.New("google client ID and client secret are required")
	}
	parsedCallback, err := url.Parse(callbackURL)
	if err != nil || parsedCallback.Scheme == "" || parsedCallback.Host == "" {
		return nil, errors.New("google callback URL must be an absolute URL")
	}
	if parsedCallback.Scheme != "https" && !(parsedCallback.Scheme == "http" && (parsedCallback.Hostname() == "localhost" || parsedCallback.Hostname() == "127.0.0.1")) {
		return nil, errors.New("google callback URL must use HTTPS except on localhost")
	}
	return &GoogleOIDCProvider{
		config: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: callbackURL,
			Endpoint: oauth2.Endpoint{
				AuthURL: googleAuthorizationEndpoint, TokenURL: googleTokenEndpoint,
				AuthStyle: oauth2.AuthStyleInParams,
			},
			Scopes: []string{"openid", "email", "profile"},
		},
		validateIDToken: idtoken.Validate,
	}, nil
}

func (p *GoogleOIDCProvider) AuthorizationURL(oauthState, nonce, codeVerifier string) string {
	return p.config.AuthCodeURL(
		oauthState,
		oauth2.S256ChallengeOption(codeVerifier),
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	)
}

func (p *GoogleOIDCProvider) VerifyAuthorization(ctx context.Context, code, codeVerifier, expectedNonce string) (GoogleIdentity, error) {
	token, err := p.config.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("exchange google authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return GoogleIdentity{}, errors.New("google token response did not include an ID token")
	}
	payload, err := p.validateIDToken(ctx, rawIDToken, p.config.ClientID)
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("validate google ID token: %w", err)
	}
	if payload.Issuer != "https://accounts.google.com" && payload.Issuer != "accounts.google.com" {
		return GoogleIdentity{}, errors.New("google ID token has an invalid issuer")
	}
	nonce, ok := payload.Claims["nonce"].(string)
	if !ok || subtle.ConstantTimeCompare([]byte(nonce), []byte(expectedNonce)) != 1 {
		return GoogleIdentity{}, errors.New("google ID token nonce did not match")
	}
	email, ok := payload.Claims["email"].(string)
	if !ok {
		return GoogleIdentity{}, errors.New("google ID token did not include an email")
	}
	normalizedEmail, err := validateEmail(email)
	if err != nil {
		return GoogleIdentity{}, errors.New("google ID token included an invalid email")
	}
	emailVerified, ok := payload.Claims["email_verified"].(bool)
	if !ok || !emailVerified {
		return GoogleIdentity{}, errors.New("google email is not verified")
	}
	if payload.Subject == "" || len(payload.Subject) > 255 {
		return GoogleIdentity{}, errors.New("google ID token has an invalid subject")
	}
	displayName, _ := payload.Claims["name"].(string)
	if strings.TrimSpace(displayName) == "" {
		displayName = strings.Split(normalizedEmail, "@")[0]
	}
	displayName, err = validateDisplayName(displayName)
	if err != nil {
		return GoogleIdentity{}, errors.New("google ID token has an invalid display name")
	}
	var avatarURL *string
	if picture, ok := payload.Claims["picture"].(string); ok && len(picture) <= 2048 {
		parsedPicture, parseErr := url.Parse(picture)
		if parseErr == nil && parsedPicture.Scheme == "https" && parsedPicture.Host != "" {
			avatarURL = &picture
		}
	}
	return GoogleIdentity{
		Subject: payload.Subject, Email: normalizedEmail, EmailVerified: true,
		DisplayName: displayName, AvatarURL: avatarURL,
	}, nil
}
