// Package controlapi defines the implemented loopback enrollment wire types.
package controlapi

import "time"

type Invitation struct {
	InvitationID  string    `json:"invitation_id"`
	Secret        string    `json:"secret"`
	ControllerURL string    `json:"controller_url"`
	CAPin         string    `json:"ca_pin"`
	CACertPEM     string    `json:"ca_cert_pem"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type ClaimRequest struct {
	RequestedName      string   `json:"requested_name"`
	CSRPEM             string   `json:"csr_pem"`
	BootID             string   `json:"boot_id"`
	ApplicationVersion string   `json:"application_version"`
	ContractVersions   []int    `json:"contract_versions"`
	Capabilities       []string `json:"capabilities"`
}

type ClaimAccepted struct {
	EnrollmentID     string    `json:"enrollment_id"`
	ClaimToken       string    `json:"claim_token"`
	VerificationCode string    `json:"verification_code"`
	ExpiresAt        time.Time `json:"expires_at"`
	PollAfterSeconds int       `json:"poll_after_seconds"`
}

type IssuedCertificate struct {
	CertificatePEM string    `json:"certificate_pem"`
	ChainPEM       string    `json:"chain_pem"`
	Serial         string    `json:"serial"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
}

type EnrollmentStatus struct {
	Status           string             `json:"status"`
	VerificationCode string             `json:"verification_code,omitempty"`
	ExpiresAt        *time.Time         `json:"expires_at,omitempty"`
	PollAfterSeconds int                `json:"poll_after_seconds,omitempty"`
	NodeID           string             `json:"node_id,omitempty"`
	ControllerID     string             `json:"controller_id,omitempty"`
	BindingEpoch     uint64             `json:"binding_epoch,omitempty"`
	Certificate      *IssuedCertificate `json:"certificate,omitempty"`
	ContractVersion  int                `json:"contract_version,omitempty"`
}

type Presence struct {
	BootID             string    `json:"boot_id"`
	BootSequence       uint64    `json:"boot_sequence"`
	ApplicationVersion string    `json:"application_version"`
	ContractVersion    int       `json:"contract_version"`
	StateEpoch         string    `json:"state_epoch"`
	BindingEpoch       uint64    `json:"binding_epoch"`
	DesiredGeneration  uint64    `json:"desired_generation"`
	Capabilities       []string  `json:"capabilities"`
	ObservedAt         time.Time `json:"observed_at"`
}

type PresenceAccepted struct {
	ControllerID          string    `json:"controller_id"`
	SessionID             string    `json:"session_id"`
	SessionExpiresAt      time.Time `json:"session_expires_at"`
	ServerTime            time.Time `json:"server_time"`
	NextPollSeconds       int       `json:"next_poll_seconds"`
	RenewCertificateAfter time.Time `json:"renew_certificate_after"`
}
