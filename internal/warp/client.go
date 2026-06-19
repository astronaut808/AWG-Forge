package warp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

const (
	DefaultAPIBaseURL       = "https://api.cloudflareclient.com/v0a4005"
	DefaultAPIClientVersion = "a-6.30-3596"
)

type APIClient struct {
	BaseURL       string
	ClientVersion string
	HTTPClient    *http.Client
}

func DefaultAPIClient() APIClient {
	return APIClient{
		BaseURL:       DefaultAPIBaseURL,
		ClientVersion: DefaultAPIClientVersion,
		HTTPClient:    &http.Client{Timeout: 15 * time.Second},
	}
}

func Register(ctx context.Context, privateKey, publicKey string) (config.Warp, error) {
	return DefaultAPIClient().Register(ctx, privateKey, publicKey)
}

func (c APIClient) Register(ctx context.Context, privateKey, publicKey string) (config.Warp, error) {
	privateKey = strings.TrimSpace(privateKey)
	publicKey = strings.TrimSpace(publicKey)
	if privateKey == "" || publicKey == "" {
		return config.Warp{}, errors.New("WARP key pair is required")
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	version := strings.TrimSpace(c.ClientVersion)
	if version == "" {
		version = DefaultAPIClientVersion
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	body := registerRequest{
		Key:   publicKey,
		ToS:   time.Now().UTC().Format(time.RFC3339),
		Type:  "PC",
		Model: "awg-forge",
		Name:  "awg-forge",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return config.Warp{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/reg", bytes.NewReader(raw))
	if err != nil {
		return config.Warp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("CF-Client-Version", version)

	res, err := httpClient.Do(req)
	if err != nil {
		return config.Warp{}, fmt.Errorf("WARP registration request failed: %w", err)
	}
	defer func() {
		_ = res.Body.Close()
	}()
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return config.Warp{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return config.Warp{}, fmt.Errorf("WARP registration failed: %s", apiErrorMessage(responseBody, res.Status))
	}

	var out registerResponse
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return config.Warp{}, fmt.Errorf("WARP registration response is invalid: %w", err)
	}
	warpConfig := config.Warp{
		InterfaceName:       DefaultInterfaceName,
		DeviceID:            strings.TrimSpace(out.ID),
		AccessToken:         strings.TrimSpace(out.Token),
		LicenseKey:          strings.TrimSpace(out.Account.License),
		ClientID:            strings.TrimSpace(out.Config.ClientID),
		PrivateKey:          privateKey,
		PeerPublicKey:       strings.TrimSpace(firstPeer(out).PublicKey),
		Endpoint:            strings.TrimSpace(firstPeer(out).Endpoint.Host),
		AddressV4:           firstAddressV4(out.Config.Interface.Addresses),
		AddressV6:           firstAddressV6(out.Config.Interface.Addresses),
		MTU:                 1280,
		PersistentKeepalive: 25,
		RegisteredAt:        time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if err := Validate(warpConfig); err != nil {
		return config.Warp{}, fmt.Errorf("WARP registration response is incomplete: %w", err)
	}
	if !warpConfig.Registered() {
		return config.Warp{}, errors.New("WARP registration response is missing device credentials")
	}
	return warpConfig, nil
}

type registerRequest struct {
	Key   string `json:"key"`
	ToS   string `json:"tos"`
	Type  string `json:"type"`
	Model string `json:"model"`
	Name  string `json:"name"`
}

type registerResponse struct {
	ID      string `json:"id"`
	Token   string `json:"token"`
	Account struct {
		License string `json:"license"`
	} `json:"account"`
	Config struct {
		ClientID  string `json:"client_id"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
		Peers []registerPeer `json:"peers"`
	} `json:"config"`
}

type registerPeer struct {
	PublicKey string `json:"public_key"`
	Endpoint  struct {
		Host string `json:"host"`
	} `json:"endpoint"`
}

type apiErrorResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Message string `json:"message"`
}

func firstPeer(response registerResponse) registerPeer {
	if len(response.Config.Peers) == 0 {
		return registerPeer{}
	}
	return response.Config.Peers[0]
}

func firstAddressV4(addresses struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}) string {
	return strings.TrimSuffix(strings.TrimSpace(addresses.V4), "/32")
}

func firstAddressV6(addresses struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}) string {
	return strings.TrimSpace(addresses.V6)
}

func apiErrorMessage(body []byte, fallback string) string {
	var parsed apiErrorResponse
	if err := json.Unmarshal(body, &parsed); err == nil {
		for _, item := range parsed.Errors {
			if strings.TrimSpace(item.Message) != "" {
				return item.Message
			}
		}
		if strings.TrimSpace(parsed.Message) != "" {
			return parsed.Message
		}
	}
	return fallback
}
