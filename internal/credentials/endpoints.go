package credentials

import (
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/cloudsign"
)

// Read from the daemon's environment only; a stored credential must never choose where the daemon
// calls (SSRF).
const (
	envAWSSTSEndpoint    = "OCTOPUS_AWS_STS_ENDPOINT"
	envAWSIAMEndpoint    = "OCTOPUS_AWS_IAM_ENDPOINT"
	envAWSSigningRegion  = "OCTOPUS_AWS_SIGNING_REGION"
	envGCPOAuth2Endpoint = "OCTOPUS_GCP_OAUTH2_ENDPOINT"
	envGCPCRMEndpoint    = "OCTOPUS_GCP_CRM_ENDPOINT"
	defaultSigningRegion = "us-east-1"
)

func providerEndpoint(getenv func(string) string, env, def string) (string, error) {
	return cloudsign.Endpoint(getenv, env, def)
}

func awsSTSURL() (string, error) {
	return providerEndpoint(os.Getenv, envAWSSTSEndpoint, awsSTSEndpoint)
}

func awsIAMURL() (string, error) {
	return providerEndpoint(os.Getenv, envAWSIAMEndpoint, awsIAMEndpoint)
}

// The assertion audience stays the canonical token URI: Google checks it against its own endpoint
// regardless of the override.
func gcpTokenURL() (string, error) {
	return providerEndpoint(os.Getenv, envGCPOAuth2Endpoint, gcpTokenEndpoint)
}

func gcpCRMURL() (string, error) {
	u, err := providerEndpoint(os.Getenv, envGCPCRMEndpoint, gcpCRMEndpoint)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u, nil
}

// Global STS/IAM sign against us-east-1; GovCloud and China partitions need their own region
// alongside an endpoint override.
func awsSigningRegionFor(getenv func(string) string) string {
	if r := strings.TrimSpace(getenv(envAWSSigningRegion)); r != "" {
		return r
	}
	return defaultSigningRegion
}
