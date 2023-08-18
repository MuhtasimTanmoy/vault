// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package certutil

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// Source of the issuance request: sign implies that the key material was
// generated by the user and submitted via a CSR request but only ACL level
// validation was applied; issue implies that Vault created the key material
// on behalf of the user with ACL level validation occurring; ACME implies
// that the user submitted a CSR and that additional ACME validation has
// occurred before sending the request to the external service for
// construction.
type CIEPSIssuanceMode string

const (
	SignCIEPSMode  = "sign"
	IssueCIEPSMode = "issue"
	ACMECIEPSMode  = "acme"
	ICACIEPSMode   = "ica"
)

// Configuration of the issuer and mount at the time of this request;
// states the issuer's templated AIA information (falling back to the
// mount-global config if no per-issuer AIA info is set, the issuer's
// leaf_not_after_behavior (permit/truncate/err) for TTLs exceeding the
// issuer's validity period, and the mount's default and max TTL.
type CIEPSIssuanceConfig struct {
	AIAValues            *URLEntries `json:"aia_values"`
	LeafNotAfterBehavior string      `json:"leaf_not_after_behavior"`
	MountDefaultTTL      string      `json:"mount_default_ttl"`
	MountMaxTTL          string      `json:"mount_max_ttl"`
}

// Structured parameters sent by Vault or explicitly validated by Vault
// prior to sending.
type CIEPSVaultParams struct {
	PolicyName string `json:"policy_name,omitempty"`
	Mount      string `json:"mount"`
	Namespace  string `json:"ns"`

	// These indicate the type of the cluster node talking to the CIEPS
	// service. When IsPerfStandby=true, setting StoreCert=true in the
	// response will result in Vault forwarding the client's request
	// up to the Performance Secondary's active node and re-trying the
	// operation (including re-submitting the request to the CIEPS
	// service).
	//
	// Any response returned by the CIEPS service in this case will be
	// ignored and not signed by the CA's keys.
	//
	// IsPRSecondary is set to false when a local mount is used on a
	// PR Secondary; in this scenario, PR Secondary nodes behave like
	// PR Primary nodes. From a CIEPS service perspective, no behavior
	// difference is expected between PR Primary and PR Secondary nodes;
	// both will issue and store certificates on their active nodes.
	// This information is included for audit tracking purposes.
	IsPerfStandby bool `json:"vault_is_performance_standby"`
	IsPRSecondary bool `json:"vault_is_performance_secondary"`

	IssuanceMode CIEPSIssuanceMode `json:"issuance_mode"`

	GeneratedKey bool `json:"vault_generated_private_key"`

	IssuerName string `json:"requested_issuer_name"`
	IssuerID   string `json:"requested_issuer_id"`
	IssuerCert string `json:"requested_issuer_cert"`

	Config CIEPSIssuanceConfig `json:"requested_issuance_config"`
}

// Outer request object sent by Vault to the external CIEPS service.
//
// The top-level fields denote properties about the CIEPS request,
// with various request fields containing untrusted and trusted input
// respectively.
type CIEPSRequest struct {
	Version int    `json:"request_version"`
	UUID    string `json:"request_uuid"`
	Sync    bool   `json:"synchronous"`

	UserRequestKV     map[string]interface{} `json:"user_request_key_values"`
	IdentityRequestKV map[string]interface{} `json:"identity_request_key_values,omitempty"`
	ACMERequestKV     map[string]interface{} `json:"acme_request_key_values,omitempty"`
	VaultRequestKV    CIEPSVaultParams       `json:"vault_request_values"`

	// Vault guarantees that UserRequestKV will contain a csr parameter
	// for all request types; this field is useful for engine implementations
	// to have in parsed format. We assume that this is sent in PEM format,
	// aligning with other Vault requests.
	ParsedCSR *x509.CertificateRequest `json:"-"`
}

func (req *CIEPSRequest) ParseUserCSR() error {
	csrValueRaw, present := req.UserRequestKV["csr"]
	if !present {
		return fmt.Errorf("missing expected 'csr' attribute on the request")
	}

	csrValue, ok := csrValueRaw.(string)
	if !ok {
		return fmt.Errorf("unexpected type of 'csr' attribute: %T", csrValueRaw)
	}

	if csrValue == "" {
		return fmt.Errorf("unexpectedly empty 'csr' attribute on the request")
	}

	block, rest := pem.Decode([]byte(csrValue))
	if len(rest) > 0 {
		return fmt.Errorf("failed to decode 'csr': %v bytes of trailing data after PEM block", len(rest))
	}
	if block == nil {
		return fmt.Errorf("failed to decode 'csr' PEM block")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate request: %w", err)
	}

	req.ParsedCSR = csr
	return nil
}

// Expected response object from the external CIEPS service.
//
// When parsing, Vault will disallow unknown fields, failing the
// parse if unknown fields are sent.
type CIEPSResponse struct {
	UUID              string            `json:"request_uuid"`
	Error             string            `json:"error,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	Certificate       string            `json:"certificate"`
	ParsedCertificate *x509.Certificate `json:"-"`
	IssuerRef         string            `json:"issuer_ref"`
	StoreCert         bool              `json:"store_certificate"`
	GenerateLease     bool              `json:"generate_lease"`
}

func (c *CIEPSResponse) MarshalCertificate() error {
	if c.ParsedCertificate == nil || len(c.ParsedCertificate.Raw) == 0 {
		return fmt.Errorf("no certificate present")
	}

	pem := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: c.ParsedCertificate.Raw,
	})
	if len(pem) == 0 {
		return fmt.Errorf("failed to generate PEM: no body")
	}
	c.Certificate = string(pem)

	return nil
}
