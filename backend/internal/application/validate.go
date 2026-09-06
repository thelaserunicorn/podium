package application

import (
	"net/url"
	"regexp"
	"strings"
)

// dns1123 mirrors the Kubernetes DNS-1123 label rules used for namespace
// names. Per DECISIONS.md C and PLAN.md risk #5, custom namespaces must
// match this; we apply the same rule to keep Podium and Kubernetes aligned.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// appNameRe limits application names to a DNS-friendly subset. Kubernetes
// resource names share constraints, and the per-user Deployment name is
// derived from the application name (DECISIONS.md E).
var appNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateNamespaceName returns nil if name is a valid Kubernetes namespace
// identifier (DNS-1123 label, 1-63 chars). Empty or too-long names are
// rejected along with anything outside [a-z0-9-].
func ValidateNamespaceName(name string) error {
	if len(name) < 1 || len(name) > 63 {
		return ErrInvalidNamespace
	}
	if !dns1123.MatchString(name) {
		return ErrInvalidNamespace
	}
	return nil
}

// ValidateName returns nil if name is a valid application name.
func ValidateName(name string) error {
	if len(name) < 1 || len(name) > 63 {
		return ErrInvalidName
	}
	if !appNameRe.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

// ValidateRepositoryURL accepts http(s) URLs only. Private repos are
// out of scope per DECISIONS.md A so we do not check for auth tokens.
func ValidateRepositoryURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ErrInvalidRepoURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrInvalidRepoURL
	}
	if u.Host == "" {
		return ErrInvalidRepoURL
	}
	return nil
}

// ValidatePort accepts 1..65535. Port 0 is reserved ("let the kernel pick")
// and would break the per-app Service mapping (spec.md §17).
func ValidatePort(p int) error {
	if p < 1 || p > 65535 {
		return ErrInvalidPort
	}
	return nil
}
