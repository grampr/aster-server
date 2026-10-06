package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/grampr/aster-server/internal/mail"
)

const (
	verificationLifetime = 24 * time.Hour
	resetLifetime        = time.Hour
	// emailCooldown stops one User from being mailed repeatedly.
	emailCooldown  = time.Minute
	mailSendBudget = 30 * time.Second

	verifyEmailLink   = "aster://auth/verify-email"
	resetPasswordLink = "aster://auth/reset-password"
)

// WithMailer enables the flows that send email: verification and password reset.
func (s *Service) WithMailer(mailer mail.Mailer, logger *slog.Logger) *Service {
	s.mailer, s.logger = mailer, logger
	return s
}

// WaitForMail blocks until every email queued so far has been handled. Tests use it;
// the Server never needs to wait.
func (s *Service) WaitForMail() { s.mailWait.Wait() }

// background runs work detached from the request. Sending email asynchronously keeps
// the response time the same whether or not an Account exists.
func (s *Service) background(work func(ctx context.Context)) {
	s.mailWait.Add(1)
	go func() {
		defer s.mailWait.Done()
		ctx, cancel := context.WithTimeout(context.Background(), mailSendBudget)
		defer cancel()
		work(ctx)
	}()
}

func (s *Service) logFailure(message string, err error) {
	if s.logger != nil {
		s.logger.Error(message, "error", err)
	}
}

// RequestEmailVerification mails the User a token to confirm their address.
func (s *Service) RequestEmailVerification(ctx context.Context, user User) error {
	if s.mailer == nil {
		return ErrMailUnavailable
	}
	if user.EmailVerified {
		return nil
	}
	email := normalizeEmail(user.Email)
	s.background(func(ctx context.Context) {
		s.sendToken(ctx, user.ID, user.Email, email, PurposeVerifyEmail, verificationLifetime, verifyEmailLink,
			"【Aster】メールアドレスの確認 / Confirm your email address",
			"Aster のメールアドレスを確認するには、次の確認コードをアプリに入力するか、リンクを開いてください。\n"+
				"To confirm your email address for Aster, enter this code in the app or open the link.\n\n")
	})
	return nil
}

// VerifyEmail confirms the address the token was sent to.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	if !plausibleToken(token) {
		return ErrInvalidToken
	}
	return s.store.VerifyEmail(ctx, HashToken(token), s.now().UTC())
}

// RequestPasswordReset mails a reset token if an Account with a Password exists for
// the address. It reports nothing about whether one does.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	if s.mailer == nil {
		return ErrMailUnavailable
	}
	normalized, err := validateEmail(email)
	if err != nil {
		return err
	}
	s.background(func(ctx context.Context) {
		identity, err := s.store.FindPasswordIdentity(ctx, normalized)
		if errors.Is(err, errIdentityNotFound) {
			return
		}
		if err != nil {
			s.logFailure("find account for password reset", err)
			return
		}
		s.sendToken(ctx, identity.User.ID, identity.User.Email, normalized, PurposeResetPassword, resetLifetime, resetPasswordLink,
			"【Aster】パスワードの再設定 / Reset your password",
			"Aster のパスワードを再設定するには、次の確認コードをアプリに入力するか、リンクを開いてください。心当たりがない場合は、このメールを無視してください。\n"+
				"To reset your Aster password, enter this code in the app or open the link. If you did not ask for this, ignore this email.\n\n")
	})
	return nil
}

// ResetPassword sets a new Password and signs the User out everywhere. The Password is
// checked first so a typo does not spend the token.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	if !plausibleToken(token) {
		return ErrInvalidToken
	}
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.store.ResetPassword(ctx, HashToken(token), hash, s.now().UTC())
}

// sendToken stores a token and mails it. A User inside the cooldown gets nothing new.
func (s *Service) sendToken(ctx context.Context, userID uuid.UUID, address, normalized, purpose string, lifetime time.Duration, link, subject, intro string) {
	token, err := randomToken()
	if err != nil {
		s.logFailure("generate email token", err)
		return
	}
	id, err := newUUIDv7()
	if err != nil {
		s.logFailure("generate email token ID", err)
		return
	}
	now := s.now().UTC()
	created, err := s.store.CreateEmailToken(ctx, EmailToken{
		ID: id, UserID: userID, Purpose: purpose, Email: normalized, TokenHash: HashToken(token),
		CreatedAt: now, ExpiresAt: now.Add(lifetime),
	}, emailCooldown)
	if err != nil {
		s.logFailure("store email token", err)
		return
	}
	if !created {
		return
	}
	deepLink := link + "?" + url.Values{"token": {token}}.Encode()
	body := intro + "確認コード / Code: " + token + "\nリンク / Link: " + deepLink + "\n\n" +
		fmt.Sprintf("このコードは %d 分間有効です。 / This code is valid for %d minutes.\n", int(lifetime/time.Minute), int(lifetime/time.Minute))
	if err := s.mailer.Send(ctx, mail.Message{To: address, Subject: subject, Body: body}); err != nil {
		s.logFailure("send email", err)
	}
}

// LinkGoogleIdentity adds the Google Identity from a link attempt to the signed-in User.
func (s *Service) LinkGoogleIdentity(ctx context.Context, userID uuid.UUID, exchangeCode, codeVerifier string) (User, error) {
	grant, err := s.redeemExchangeCode(ctx, exchangeCode, codeVerifier)
	if err != nil {
		return User{}, err
	}
	if grant.LinkUserID == nil || *grant.LinkUserID != userID {
		return User{}, ErrInvalidAuthorizationGrant
	}
	identityID, err := newUUIDv7()
	if err != nil {
		return User{}, err
	}
	return s.store.LinkGoogle(ctx, LinkGoogleInput{UserID: userID, IdentityID: identityID, Identity: grant.Identity, Now: s.now().UTC()})
}

// UnlinkAuthenticationMethod removes PASSWORD or GOOGLE from the User's Account.
func (s *Service) UnlinkAuthenticationMethod(ctx context.Context, userID uuid.UUID, method string) error {
	if method != ProviderPassword && method != ProviderGoogle {
		return ErrAuthMethodNotLinked
	}
	return s.store.UnlinkAuthenticationMethod(ctx, userID, method)
}

func plausibleToken(token string) bool { return len(token) >= 32 && len(token) <= 512 }
