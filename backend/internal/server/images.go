package server

import (
	"fmt"
	"regexp"
	"strings"
)

// ImageConfig says where the agent image and chart in an install command come from. The three parts belong
// together: a tag or digest names something inside a registry, so they are only meaningful with one. Nothing here
// has a default; an operator (or an organisation's administrator) chooses, because whoever controls the registry
// controls what runs in every cluster that installs from it.
type ImageConfig struct {
	// Registry is host[:port][/namespace] (registry.example.com/team), or a bare Docker Hub namespace (myteam). Lowercase, no scheme.
	Registry string `json:"registry"`
	// Tag is a mutable label; empty means the chart's own appVersion.
	Tag string `json:"tag"`
	// Digest is sha256:<64 hex>. When set the install pins the image by it, and the tag is ignored by the chart.
	Digest string `json:"digest"`
}

// Configured reports whether an image registry is set, which is what switches the install command from the chart's
// built-in names (and a downloaded chart file) to pulling image and chart from that registry.
func (c ImageConfig) Configured() bool { return c.Registry != "" }

var (
	// A registry host: DNS-ish, with an optional port. A bare Docker Hub namespace also matches.
	registryHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	// A path component of a repository name, as the OCI distribution spec defines it.
	registryPart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	imageTagRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	imageDigest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// CheckImageRegistry validates a registry setting and returns it trimmed (surrounding spaces and trailing slashes
// removed). The checks also keep it safe to print inside a shell command and a helm --set value.
func CheckImageRegistry(v string) (string, error) {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return "", nil
	}
	switch {
	case len(v) > 255:
		return "", fmt.Errorf("the image registry is at most 255 characters")
	case strings.Contains(v, "://"):
		return "", fmt.Errorf("write the image registry without a scheme (registry.example.com/team, not https://registry.example.com/team)")
	case v != strings.ToLower(v):
		return "", fmt.Errorf("the image registry must be lowercase")
	case strings.ContainsAny(v, " \t\r\n@?#,'\"\\$`;&|<>(){}*~!"):
		return "", fmt.Errorf("the image registry may not contain spaces or the characters @ ? # , and shell symbols")
	}
	parts := strings.Split(v, "/")
	if !registryHost.MatchString(parts[0]) {
		return "", fmt.Errorf("%q is not a registry host (registry.example.com, registry.example.com:5000 or a Docker Hub name)", parts[0])
	}
	for _, p := range parts[1:] {
		if !registryPart.MatchString(p) {
			return "", fmt.Errorf("%q is not a valid registry path segment (lowercase letters, digits, and single . _ - between them)", p)
		}
	}
	return v, nil
}

// CheckImageTag validates an image tag (Docker's tag character set, at most 128 characters).
func CheckImageTag(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v != "" && !imageTagRe.MatchString(v) {
		return "", fmt.Errorf("the image tag may use letters, digits, _ . - only, up to 128 characters, and cannot start with . or -")
	}
	return v, nil
}

// CheckImageDigest validates an image digest: sha256: and 64 lowercase hex digits.
func CheckImageDigest(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v != "" && !imageDigest.MatchString(v) {
		return "", fmt.Errorf("the image digest must be sha256: followed by 64 lowercase hex digits")
	}
	return v, nil
}

// Normalize validates all three parts together and returns them cleaned up.
func (c ImageConfig) Normalize() (ImageConfig, error) {
	var err error
	if c.Registry, err = CheckImageRegistry(c.Registry); err != nil {
		return c, err
	}
	if c.Tag, err = CheckImageTag(c.Tag); err != nil {
		return c, err
	}
	if c.Digest, err = CheckImageDigest(c.Digest); err != nil {
		return c, err
	}
	if c.Registry == "" && (c.Tag != "" || c.Digest != "") {
		return c, fmt.Errorf("set an image registry first: a tag or digest names an image inside it")
	}
	return c, nil
}

// images is the image configuration an organisation's install commands use. Precedence: the organisation's own
// setting (Settings → Installation), then the server's --image-* flags, then nothing (the chart's built-in names).
// The three parts are taken together from one source, so a digest saved for a registry is never combined with a
// different registry from a flag.
func (a *Admin) images(c *Core) ImageConfig {
	if c != nil {
		if s := c.Settings(); s.ImageRegistry != "" {
			return ImageConfig{s.ImageRegistry, s.ImageTag, s.ImageDigest}
		}
	}
	return a.imageDefaults()
}

// imageDefaults is what the server's flags (or environment) say, used when an organisation has set nothing.
func (a *Admin) imageDefaults() ImageConfig {
	return ImageConfig{strings.TrimRight(a.ImageRegistry, "/"), a.ImageTag, a.ImageDigest}
}
