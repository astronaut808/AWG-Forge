package controlapi

// CertificateRenewalRequest is authenticated by the presented mTLS peer. The
// boot ID is process metadata only; CurrentSerial must match that peer leaf.
type CertificateRenewalRequest struct {
	BootID        string `json:"boot_id"`
	CurrentSerial string `json:"current_serial"`
	CSRPEM        string `json:"csr_pem"`
}
