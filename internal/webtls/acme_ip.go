package webtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acme "github.com/astronaut808/acme/v3"
	"github.com/astronaut808/awg-forge/internal/config"
)

const (
	acmeIPProfile        = "shortlived"
	acmeIPRenewBefore    = 72 * time.Hour
	acmeIPRetryMin       = time.Minute
	acmeIPRetryMax       = time.Hour
	acmeIPRequestTimeout = 90 * time.Second
)

type acmeIPState struct {
	mu     sync.RWMutex
	cert   *tls.Certificate
	status Status
	tokens map[string]string
	issue  func() (*tls.Certificate, error)
}

func buildACMEIPRuntime(cfg config.Config, runtime Runtime) (Runtime, error) {
	ip, err := normalizePublicIP(runtime.Settings.ACMEIP)
	if err != nil {
		return Runtime{}, err
	}
	runtime.Settings.ACMEIP = ip.String()
	acmeDir := filepath.Join(cfg.ConfigDir, filepath.FromSlash(ACMECacheRelativePath))
	if err := ensurePrivateDirectory(acmeDir); err != nil {
		return Runtime{}, err
	}
	cacheDir := filepath.Join(acmeDir, "ip")
	if err := ensurePrivateDirectory(cacheDir); err != nil {
		return Runtime{}, err
	}
	state := &acmeIPState{status: Status{Mode: ModeACMEIP, IP: ip.String(), State: "pending"}, tokens: make(map[string]string)}
	cachedStatus, hasCachedStatus, err := loadACMEIPStatus(cacheDir, ip)
	if err != nil {
		return Runtime{}, err
	}
	if pair, status, ok, err := loadACMEIPCertificate(cacheDir, ip); err != nil {
		return Runtime{}, err
	} else if ok {
		if hasCachedStatus && cachedStatus.State == "active" {
			status.Warning = cachedStatus.Warning
			status.LastAttempt = cachedStatus.LastAttempt
			status.NextAttempt = cachedStatus.NextAttempt
			status.AttemptCount = cachedStatus.AttemptCount
		}
		state.cert, state.status = &pair, status
	} else if hasCachedStatus && cachedStatus.State != "active" {
		state.status = cachedStatus
	}
	state.issue = func() (*tls.Certificate, error) {
		return issueACMEIPCertificate(runtime.Settings, cacheDir, state)
	}
	runtime.Status = state.readStatus()
	runtime.TLSConfig = &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			state.mu.RLock()
			defer state.mu.RUnlock()
			if state.cert == nil {
				return nil, errors.New("ACME IP certificate is pending")
			}
			return state.cert, nil
		},
	}
	runtime.ACMEHTTPHandler = state.httpHandler()
	runtime.statusReader = state.readStatus
	runtime.start = func(ctx context.Context) { go state.renew(ctx, cacheDir) }
	return runtime, nil
}

func (s *acmeIPState) renew(ctx context.Context, cacheDir string) {
	for {
		next := s.nextAttemptDelay(time.Now())
		if next > 0 {
			timer := time.NewTimer(next)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		now := time.Now().UTC()
		pair, err := s.issue()
		if err != nil {
			s.recordFailure(cacheDir, now)
			continue
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			s.recordFailure(cacheDir, now)
			continue
		}
		if err := saveACMEIPCertificate(cacheDir, *pair); err != nil {
			s.recordFailure(cacheDir, now)
			continue
		}
		s.mu.Lock()
		s.cert = pair
		s.status.Subject = leaf.Subject.String()
		s.status.Issuer = leaf.Issuer.String()
		s.status.NotBefore = leaf.NotBefore.UTC()
		s.status.NotAfter = leaf.NotAfter.UTC()
		s.status.State = "active"
		s.status.Error = ""
		s.status.Warning = ""
		s.status.LastAttempt = now
		s.status.NextAttempt = time.Time{}
		s.status.AttemptCount = 0
		status := s.status
		s.mu.Unlock()
		_ = saveACMEIPStatus(cacheDir, status)
	}
}

func (s *acmeIPState) nextAttemptDelay(now time.Time) time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.status.NextAttempt.IsZero() {
		if delay := s.status.NextAttempt.Sub(now); delay > 0 {
			return delay
		}
		return 0
	}
	if s.cert == nil || s.status.NotAfter.IsZero() {
		return 0
	}
	if delay := s.status.NotAfter.Add(-acmeIPRenewBefore).Sub(now); delay > 0 {
		return delay
	}
	return 0
}

func (s *acmeIPState) recordFailure(cacheDir string, now time.Time) {
	s.mu.Lock()
	s.status.LastAttempt = now
	s.status.AttemptCount++
	s.status.NextAttempt = now.Add(acmeIPRetryDelay(s.status.AttemptCount))
	if s.cert == nil {
		s.status.State = "failed"
		s.status.Error = "certificate issuance failed"
		s.status.Warning = ""
	} else {
		s.status.Error = ""
		s.status.Warning = "certificate renewal failed"
	}
	status := s.status
	s.mu.Unlock()
	_ = saveACMEIPStatus(cacheDir, status)
}

func acmeIPRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return acmeIPRetryMin
	}
	delay := acmeIPRetryMin
	for count := 1; count < attempt && delay < acmeIPRetryMax; count++ {
		delay *= 2
	}
	if delay > acmeIPRetryMax {
		return acmeIPRetryMax
	}
	return delay
}

func (s *acmeIPState) httpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		const prefix = "/.well-known/acme-challenge/"
		if !strings.HasPrefix(r.URL.Path, prefix) || strings.Contains(strings.TrimPrefix(r.URL.Path, prefix), "/") {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, prefix)
		s.mu.RLock()
		keyAuthorization, ok := s.tokens[token]
		s.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// HTTP-01 requires the exact ACME key authorization as a text/plain response.
		_, _ = w.Write([]byte(keyAuthorization)) // nosemgrep: go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter, go.net.xss.no-direct-write-to-responsewriter-taint.no-direct-write-to-responsewriter-taint
	})
}

func (s *acmeIPState) readStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func issueACMEIPCertificate(settings Settings, cacheDir string, state *acmeIPState) (*tls.Certificate, error) {
	ip, err := normalizePublicIP(settings.ACMEIP)
	if err != nil {
		return nil, err
	}
	client, err := acme.NewClient(acme.LetsEncryptProduction, acme.WithHTTPTimeout(acmeIPRequestTimeout), acme.WithUserAgentSuffix("awg-forge"))
	if err != nil {
		return nil, err
	}
	accountKey, err := loadOrCreateACMEAccountKey(cacheDir)
	if err != nil {
		return nil, err
	}
	account, err := client.NewAccountOptions(accountKey, acme.NewAcctOptAgreeTOS(), acme.NewAcctOptWithContacts("mailto:"+settings.ACMEEmail))
	if err != nil {
		return nil, err
	}
	order, err := client.NewOrderExtension(account, []acme.Identifier{{Type: "ip", Value: ip.String()}}, acme.OrderExtension{Profile: acmeIPProfile})
	if err != nil {
		return nil, err
	}
	for _, authorizationURL := range order.Authorizations {
		authorization, err := client.FetchAuthorization(account, authorizationURL)
		if err != nil {
			return nil, err
		}
		challenge, ok := authorization.ChallengeMap["http-01"]
		if !ok {
			return nil, errors.New("ACME server did not offer HTTP-01 for IP certificate")
		}
		state.mu.Lock()
		state.tokens[challenge.Token] = challenge.KeyAuthorization
		state.mu.Unlock()
		_, err = client.UpdateChallenge(account, challenge)
		state.mu.Lock()
		delete(state.tokens, challenge.Token)
		state.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	order, err = client.FetchOrder(account, order.URL)
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{IPAddresses: []net.IP{net.IP(ip.AsSlice())}}, key)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	order, err = client.FinalizeOrder(account, order, csr)
	if err != nil {
		return nil, err
	}
	chain, err := client.FetchCertificates(account, order.Certificate)
	if err != nil || len(chain) == 0 {
		return nil, fmt.Errorf("ACME certificate download failed")
	}
	if err := chain[0].VerifyHostname(ip.String()); err != nil {
		return nil, errors.New("issued certificate does not match configured IP")
	}
	return &tls.Certificate{Certificate: certificateDER(chain), PrivateKey: key, Leaf: chain[0]}, nil
}

func certificateDER(chain []*x509.Certificate) [][]byte {
	result := make([][]byte, 0, len(chain))
	for _, cert := range chain {
		result = append(result, cert.Raw)
	}
	return result
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("ACME cache path must be a directory, not a symlink")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect ACME cache directory")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("cannot create ACME cache directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.New("cannot secure ACME cache directory")
	}
	return nil
}

func loadOrCreateACMEAccountKey(cacheDir string) (*ecdsa.PrivateKey, error) {
	path := filepath.Join(cacheDir, "account-key.pem")
	if _, err := os.Lstat(path); err == nil {
		if err := checkRegularFile(path, true); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, errors.New("invalid ACME account key")
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid ACME account key")
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect ACME account key")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := writePrivateFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})); err != nil {
		return nil, err
	}
	return key, nil
}

func saveACMEIPCertificate(cacheDir string, pair tls.Certificate) error {
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("ACME certificate key is not ECDSA")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	for _, der := range pair.Certificate {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return writePrivateFile(filepath.Join(cacheDir, "certificate.pem"), data)
}

func loadACMEIPCertificate(cacheDir string, ip netip.Addr) (tls.Certificate, Status, bool, error) {
	path := filepath.Join(cacheDir, "certificate.pem")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, Status{}, false, nil
	} else if err != nil {
		return tls.Certificate{}, Status{}, false, errors.New("cannot inspect ACME IP certificate cache")
	}
	if err := checkRegularFile(path, true); err != nil {
		return tls.Certificate{}, Status{}, false, fmt.Errorf("ACME IP certificate cache: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, Status{}, false, errors.New("cannot read ACME IP certificate cache")
	}
	pair, status, ok := parseACMEIPCertificate(data, ip)
	if !ok {
		return tls.Certificate{}, Status{}, false, nil
	}
	return pair, status, true, nil
}

func parseACMEIPCertificate(data []byte, ip netip.Addr) (tls.Certificate, Status, bool) {
	pair, err := tls.X509KeyPair(data, data)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, Status{}, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !time.Now().Before(leaf.NotAfter) || leaf.VerifyHostname(ip.String()) != nil {
		return tls.Certificate{}, Status{}, false
	}
	pair.Leaf = leaf
	return pair, Status{Mode: ModeACMEIP, IP: ip.String(), Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(), NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), State: "active"}, true
}

func loadACMEIPStatus(cacheDir string, ip netip.Addr) (Status, bool, error) {
	path := filepath.Join(cacheDir, "status.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	} else if err != nil {
		return Status{}, false, errors.New("cannot inspect ACME IP status cache")
	}
	if err := checkRegularFile(path, true); err != nil {
		return Status{}, false, fmt.Errorf("ACME IP status cache: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{}, false, errors.New("cannot read ACME IP status cache")
	}
	status, ok := parseACMEIPStatus(data, ip)
	if !ok {
		return Status{}, false, nil
	}
	return status, true, nil
}

func parseACMEIPStatus(data []byte, ip netip.Addr) (Status, bool) {
	var status Status
	if err := json.Unmarshal(data, &status); err != nil || status.Mode != ModeACMEIP || status.IP != ip.String() || (status.State != "pending" && status.State != "failed" && status.State != "active") {
		return Status{}, false
	}
	return status, true
}

func saveACMEIPStatus(cacheDir string, status Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(cacheDir, "status.json"), append(data, '\n'))
}

func writePrivateFile(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func normalizePublicIP(value string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return netip.Addr{}, errors.New("ACME IP must be a public IPv4 or IPv6 address")
	}
	return ip.Unmap(), nil
}
