package controlauth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultSessionTTL        = 30 * time.Minute
	DefaultRecentAuthTTL     = 5 * time.Minute
	DefaultRecoveryCodeCount = 10
)

var (
	ErrAlreadyInitialized = errors.New("controller authentication is already initialized")
	ErrCredentialConsumed = errors.New("controller credential was already consumed")
	ErrInvalidCredentials = errors.New("invalid controller credentials")
	ErrInvalidUsername    = errors.New("invalid controller username")
	ErrRateLimited        = errors.New("controller authentication rate limited")
	ErrSessionNotFound    = errors.New("controller session not found")
)

type RateLimitPolicy struct {
	AccountFailures int
	AccountWindow   time.Duration
	SourceFailures  int
	SourceWindow    time.Duration
	GlobalAttempts  int
	GlobalWindow    time.Duration
}

func DefaultRateLimitPolicy() RateLimitPolicy {
	return RateLimitPolicy{
		AccountFailures: 5,
		AccountWindow:   5 * time.Minute,
		SourceFailures:  20,
		SourceWindow:    5 * time.Minute,
		GlobalAttempts:  100,
		GlobalWindow:    time.Minute,
	}
}

type User struct {
	ID                   string
	Username             string
	PasswordHash         string
	TOTPSecretCiphertext string
	TOTPLastStep         int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	DisabledAt           time.Time
}

type Session struct {
	Digest          Digest
	UserID          string
	Username        string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	AuthenticatedAt time.Time
	RevokedAt       time.Time
}

type Store interface {
	CreateControllerUser(context.Context, User, []Digest) error
	FindControllerUser(context.Context, string) (User, error)
	UseControllerTOTP(context.Context, string, int64, Session, int64) error
	UseControllerRecoveryCode(context.Context, string, Digest, Session, int64) error
	FindControllerSession(context.Context, Digest, time.Time) (Session, error)
	RevokeControllerSession(context.Context, Digest, time.Time) error
	ReserveControllerAuthAttempt(context.Context, Digest, Digest, time.Time, RateLimitPolicy) (int64, error)
	FinishControllerAuthAttempt(context.Context, int64, bool, string, time.Time) error
}

type Options struct {
	PasswordParams    PasswordParams
	SessionTTL        time.Duration
	RecentAuthTTL     time.Duration
	RecoveryCodeCount int
	MaxConcurrentHash int
	RateLimits        RateLimitPolicy
	Random            io.Reader
}

type Service struct {
	store             Store
	keys              *Keys
	passwordParams    PasswordParams
	sessionTTL        time.Duration
	recentAuthTTL     time.Duration
	recoveryCodeCount int
	rateLimits        RateLimitPolicy
	random            io.Reader
	hashSlots         chan struct{}
	dummyPasswordHash string
}

type Enrollment struct {
	UserID        string
	Username      string
	RecoveryCodes []string
}

type Authentication struct {
	Token     string
	ExpiresAt time.Time
}

type Principal struct {
	UserID     string
	Username   string
	RecentAuth bool
	ExpiresAt  time.Time
}

type authenticationRequest struct {
	username     string
	password     string
	secondFactor string
	source       string
	now          time.Time
	recovery     bool
}

func NewService(store Store, keys *Keys, options Options) (*Service, error) {
	if store == nil || keys == nil {
		return nil, errors.New("controller auth service requires store and keys")
	}
	if options.PasswordParams == (PasswordParams{}) {
		options.PasswordParams = DefaultPasswordParams()
	}
	if err := validatePasswordParams(options.PasswordParams); err != nil {
		return nil, err
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = DefaultSessionTTL
	}
	if options.RecentAuthTTL == 0 {
		options.RecentAuthTTL = DefaultRecentAuthTTL
	}
	if options.RecoveryCodeCount == 0 {
		options.RecoveryCodeCount = DefaultRecoveryCodeCount
	}
	if options.MaxConcurrentHash == 0 {
		options.MaxConcurrentHash = 2
	}
	if options.RateLimits == (RateLimitPolicy{}) {
		options.RateLimits = DefaultRateLimitPolicy()
	}
	if options.SessionTTL <= 0 || options.RecentAuthTTL <= 0 || options.RecentAuthTTL > options.SessionTTL ||
		options.RecoveryCodeCount < 1 || options.RecoveryCodeCount > 32 || options.MaxConcurrentHash < 1 || options.MaxConcurrentHash > 16 ||
		ValidateRateLimitPolicy(options.RateLimits) != nil {
		return nil, errors.New("invalid controller auth service options")
	}
	return &Service{
		store:             store,
		keys:              keys,
		passwordParams:    options.PasswordParams,
		sessionTTL:        options.SessionTTL,
		recentAuthTTL:     options.RecentAuthTTL,
		recoveryCodeCount: options.RecoveryCodeCount,
		rateLimits:        options.RateLimits,
		random:            options.Random,
		hashSlots:         make(chan struct{}, options.MaxConcurrentHash),
		dummyPasswordHash: dummyPasswordHash(options.PasswordParams),
	}, nil
}

// EnrollAdmin verifies the first TOTP code before persisting dormant
// controller credentials. Controller mode activation remains a separate step.
func (s *Service) EnrollAdmin(ctx context.Context, username, password, totpSecret, confirmationCode string, now time.Time) (Enrollment, error) {
	username, err := NormalizeUsername(username)
	if err != nil {
		return Enrollment{}, err
	}
	if err := ValidateNewPassword(password); err != nil {
		return Enrollment{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	step, err := ValidateTOTP(totpSecret, confirmationCode, now, -1)
	if err != nil {
		return Enrollment{}, ErrInvalidCredentials
	}
	passwordHash, err := s.hashPassword(ctx, password)
	if err != nil {
		return Enrollment{}, err
	}
	userUUID, err := uuid.NewRandom()
	if err != nil {
		return Enrollment{}, fmt.Errorf("generate controller user ID: %w", err)
	}
	userID := userUUID.String()
	sealedTOTP, err := s.keys.SealTOTP(userID, strings.ToUpper(strings.TrimSpace(totpSecret)), s.random)
	if err != nil {
		return Enrollment{}, err
	}
	recoveryCodes, err := NewRecoveryCodes(s.recoveryCodeCount, s.random)
	if err != nil {
		return Enrollment{}, err
	}
	recoveryDigests := make([]Digest, 0, len(recoveryCodes))
	for _, code := range recoveryCodes {
		digest, err := s.keys.RecoveryDigest(userID, code)
		if err != nil {
			return Enrollment{}, err
		}
		recoveryDigests = append(recoveryDigests, digest)
	}
	now = now.UTC()
	user := User{
		ID:                   userID,
		Username:             username,
		PasswordHash:         passwordHash,
		TOTPSecretCiphertext: sealedTOTP,
		TOTPLastStep:         step,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.store.CreateControllerUser(ctx, user, recoveryDigests); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{UserID: userID, Username: username, RecoveryCodes: recoveryCodes}, nil
}

func (s *Service) Authenticate(ctx context.Context, username, password, code, source string, now time.Time) (Authentication, error) {
	return s.authenticate(ctx, authenticationRequest{
		username:     username,
		password:     password,
		secondFactor: code,
		source:       source,
		now:          now,
	})
}

func (s *Service) AuthenticateRecovery(ctx context.Context, username, password, recoveryCode, source string, now time.Time) (Authentication, error) {
	return s.authenticate(ctx, authenticationRequest{
		username:     username,
		password:     password,
		secondFactor: recoveryCode,
		source:       source,
		now:          now,
		recovery:     true,
	})
}

func (s *Service) authenticate(ctx context.Context, request authenticationRequest) (authentication Authentication, returnErr error) {
	username := request.username
	password := request.password
	now := request.now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	attemptID, err := s.reserveAttempt(ctx, username, request.source, now)
	if err != nil {
		return Authentication{}, err
	}
	attemptSuccess := false
	attemptReason := "invalid"
	attemptFinished := false
	defer func() {
		if attemptFinished {
			return
		}
		if !errors.Is(returnErr, ErrInvalidCredentials) {
			attemptReason = "error"
		}
		_ = s.store.FinishControllerAuthAttempt(ctx, attemptID, attemptSuccess, attemptReason, time.Now().UTC())
	}()
	username, usernameErr := NormalizeUsername(username)
	var user User
	err = nil
	if usernameErr == nil {
		user, err = s.store.FindControllerUser(ctx, username)
	}
	if usernameErr != nil || errors.Is(err, ErrInvalidCredentials) {
		if dummyErr := s.verifyPassword(ctx, boundedPassword(password), s.dummyPasswordHash); dummyErr != nil {
			return Authentication{}, dummyErr
		}
		return Authentication{}, ErrInvalidCredentials
	}
	if err != nil {
		return Authentication{}, err
	}
	if len(password) > MaxPasswordBytes {
		if dummyErr := s.verifyPassword(ctx, boundedPassword(password), s.dummyPasswordHash); dummyErr != nil {
			return Authentication{}, dummyErr
		}
		return Authentication{}, ErrInvalidCredentials
	}
	valid, err := s.verifyPasswordValue(ctx, password, user.PasswordHash)
	if err != nil {
		return Authentication{}, err
	}
	if !valid || !user.DisabledAt.IsZero() {
		return Authentication{}, ErrInvalidCredentials
	}
	if request.recovery {
		digest, err := s.keys.RecoveryDigest(user.ID, request.secondFactor)
		if err != nil {
			return Authentication{}, ErrInvalidCredentials
		}
		authentication, returnErr = s.createSession(ctx, user.ID, 0, &digest, attemptID, now)
		attemptFinished = returnErr == nil
		return authentication, returnErr
	}
	secret, err := s.keys.OpenTOTP(user.ID, user.TOTPSecretCiphertext)
	if err != nil {
		return Authentication{}, err
	}
	step, err := ValidateTOTP(secret, request.secondFactor, now, user.TOTPLastStep)
	if err != nil {
		return Authentication{}, ErrInvalidCredentials
	}
	authentication, returnErr = s.createSession(ctx, user.ID, step, nil, attemptID, now)
	attemptFinished = returnErr == nil
	return authentication, returnErr
}

func (s *Service) ValidateSession(ctx context.Context, token string, now time.Time) (Principal, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	digest, err := s.keys.SessionDigest(token)
	if err != nil {
		return Principal{}, ErrSessionNotFound
	}
	session, err := s.store.FindControllerSession(ctx, digest, now.UTC())
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		UserID:     session.UserID,
		Username:   session.Username,
		RecentAuth: !session.AuthenticatedAt.IsZero() && !now.UTC().Before(session.AuthenticatedAt) && now.UTC().Sub(session.AuthenticatedAt) <= s.recentAuthTTL,
		ExpiresAt:  session.ExpiresAt,
	}, nil
}

func (s *Service) RevokeSession(ctx context.Context, token string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	digest, valid := sessionDigestForRevoke(s.keys, token)
	if !valid {
		return nil
	}
	return s.store.RevokeControllerSession(ctx, digest, now.UTC())
}

func (s *Service) createSession(ctx context.Context, userID string, totpStep int64, recoveryDigest *Digest, attemptID int64, now time.Time) (Authentication, error) {
	token, err := NewSessionToken(s.random)
	if err != nil {
		return Authentication{}, err
	}
	digest, err := s.keys.SessionDigest(token)
	if err != nil {
		return Authentication{}, err
	}
	now = now.UTC()
	session := Session{
		Digest:          digest,
		UserID:          userID,
		CreatedAt:       now,
		ExpiresAt:       now.Add(s.sessionTTL),
		AuthenticatedAt: now,
	}
	if recoveryDigest != nil {
		err = s.store.UseControllerRecoveryCode(ctx, userID, *recoveryDigest, session, attemptID)
	} else {
		err = s.store.UseControllerTOTP(ctx, userID, totpStep, session, attemptID)
	}
	if errors.Is(err, ErrCredentialConsumed) {
		return Authentication{}, ErrInvalidCredentials
	}
	if err != nil {
		return Authentication{}, err
	}
	return Authentication{Token: token, ExpiresAt: session.ExpiresAt}, nil
}

func (s *Service) hashPassword(ctx context.Context, password string) (string, error) {
	if err := s.acquireHashSlot(ctx); err != nil {
		return "", err
	}
	defer s.releaseHashSlot()
	return HashPassword(password, s.passwordParams, s.random)
}

func (s *Service) verifyPasswordValue(ctx context.Context, password, encoded string) (bool, error) {
	if err := s.acquireHashSlot(ctx); err != nil {
		return false, err
	}
	defer s.releaseHashSlot()
	return VerifyPassword(password, encoded)
}

func (s *Service) verifyPassword(ctx context.Context, password, encoded string) error {
	_, err := s.verifyPasswordValue(ctx, password, encoded)
	return err
}

func (s *Service) acquireHashSlot(ctx context.Context) error {
	select {
	case s.hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseHashSlot() {
	<-s.hashSlots
}

func (s *Service) reserveAttempt(ctx context.Context, username, source string, now time.Time) (int64, error) {
	accountKey := strings.ToLower(strings.TrimSpace(username))
	if len(accountKey) > 256 {
		accountKey = accountKey[:256]
	}
	sourceKey := strings.TrimSpace(source)
	if sourceKey == "" {
		sourceKey = "unknown"
	}
	if len(sourceKey) > 256 {
		sourceKey = sourceKey[:256]
	}
	return s.store.ReserveControllerAuthAttempt(
		ctx,
		s.keys.AccountDigest(accountKey),
		s.keys.SourceDigest(sourceKey),
		now.UTC(),
		s.rateLimits,
	)
}

func ValidateRateLimitPolicy(policy RateLimitPolicy) error {
	valid := policy.AccountFailures >= 1 && policy.AccountFailures <= 1000 &&
		policy.SourceFailures >= 1 && policy.SourceFailures <= 1000 &&
		policy.GlobalAttempts >= 1 && policy.GlobalAttempts <= 10000 &&
		policy.AccountWindow > 0 && policy.AccountWindow <= 24*time.Hour &&
		policy.SourceWindow > 0 && policy.SourceWindow <= 24*time.Hour &&
		policy.GlobalWindow > 0 && policy.GlobalWindow <= 24*time.Hour
	if !valid {
		return errors.New("invalid controller auth rate-limit policy")
	}
	return nil
}

func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 || len(username) > 64 {
		return "", ErrInvalidUsername
	}
	for _, char := range username {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return "", ErrInvalidUsername
		}
	}
	return username, nil
}

func sessionDigestForRevoke(keys *Keys, token string) (Digest, bool) {
	digest, err := keys.SessionDigest(token)
	return digest, err == nil
}

func boundedPassword(password string) string {
	if len(password) <= MaxPasswordBytes {
		return password
	}
	return password[:MaxPasswordBytes]
}

func dummyPasswordHash(params PasswordParams) string {
	salt := make([]byte, params.SaltLength)
	hash := make([]byte, params.KeyLength)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		params.Memory,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
}
