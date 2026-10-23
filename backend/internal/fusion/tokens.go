package fusion

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"ikhnos/internal/accounts"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/kernel"
	"ikhnos/internal/store"
)

// FUSION access tokens: read-only credentials for the shared data API, minted by an administrator of the server's main organisation,
// shown once, limited to the signal types, namespaces and clusters they name, and expiring.

const (
	// MaxTokens is how many tokens an organisation may hold at once.
	MaxTokens             = 50
	maxFusionTokenName    = 64
	defaultFusionTokenTTL = 90 * 24 * time.Hour
	minFusionTokenTTL     = time.Hour
	maxFusionTokenTTL     = 365 * 24 * time.Hour
	maxScopeEntries       = 50
)

var (
	namespaceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	clusterRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// TokenSpec is what a new FUSION access token is allowed.
type TokenSpec struct {
	Name       string
	Signals    []string // empty = all three
	Namespaces []string // empty = every namespace
	Clusters   []string // empty = every cluster
	TTL        time.Duration
}

func cleanScopeList(what string, in []string, re *regexp.Regexp) ([]string, error) {
	var out []string
	// (An empty list means "no limit", so one that was given but cleans down to nothing is refused below.)

	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(out, v) {
			continue
		}
		if !re.MatchString(v) {
			return nil, kernel.Errf(kernel.KindInvalid, "%q is not a valid %s", kernel.Printable(v, 40), what)
		}
		out = append(out, v)
	}
	if len(in) > 0 && len(out) == 0 {
		return nil, kernel.Errf(kernel.KindInvalid, "name at least one %s, or leave the list out to allow every one", what)
	}
	if len(out) > maxScopeEntries {
		return nil, kernel.Errf(kernel.KindInvalid, "a token can name at most %d %ss", maxScopeEntries, what)
	}
	return out, nil
}

// CreateFusionToken mints a FUSION access token. The secret is returned once and never stored.
func (c *Service) CreateFusionToken(ctx context.Context, actor string, spec TokenSpec) (string, store.FusionToken, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" || len(name) > maxFusionTokenName || kernel.Printable(name, maxFusionTokenName+1) != name {
		return "", store.FusionToken{}, kernel.Errf(kernel.KindInvalid, "give the token a name of up to %d characters", maxFusionTokenName)
	}
	signals, err := fusionapi.NormalizeSignals(spec.Signals)
	if err != nil {
		return "", store.FusionToken{}, kernel.Errf(kernel.KindInvalid, "%v", err)
	}
	namespaces, err := cleanScopeList("namespace", spec.Namespaces, namespaceRe)
	if err != nil {
		return "", store.FusionToken{}, err
	}
	clusters, err := cleanScopeList("cluster id", spec.Clusters, clusterRe)
	if err != nil {
		return "", store.FusionToken{}, err
	}
	ttl := spec.TTL
	if ttl == 0 {
		ttl = defaultFusionTokenTTL
	}
	if ttl < minFusionTokenTTL || ttl > maxFusionTokenTTL {
		return "", store.FusionToken{}, kernel.Errf(kernel.KindInvalid, "a token lasts between an hour and %d days", int(maxFusionTokenTTL.Hours()/24))
	}
	now := c.Now()
	_ = c.Store.PurgeFusionTokens(ctx, now) // a lapsed token is of no use to anyone and should not count against the cap
	existing, err := c.Store.ListFusionTokens(ctx, c.OrgID)
	if err != nil {
		return "", store.FusionToken{}, err
	}
	if len(existing) >= MaxTokens {
		return "", store.FusionToken{}, kernel.Errf(kernel.KindConflict, "there are already %d tokens; revoke one you no longer use first", MaxTokens)
	}
	secret, err := kernel.NewFusionTokenSecret()
	if err != nil {
		return "", store.FusionToken{}, kernel.Errf(kernel.KindInternal, "could not generate a token")
	}
	tok := store.FusionToken{ID: kernel.NewFusionTokenID(), OrgID: c.OrgID, Name: name, Signals: signals, Namespaces: namespaces, Clusters: clusters,
		CreatedBy: actor, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	detail := fmt.Sprintf("%s: reads %s; namespaces %s; clusters %s; expires %s", name, strings.Join(signals, ", "), scopeWords(namespaces), scopeWords(clusters), tok.ExpiresAt.Format("2006-01-02"))
	if err := c.AuditedWrite(c.Store.CreateFusionToken(ctx, tok, kernel.HashSecret(secret),
		store.WithAudit(c.AuditRow(c.OrgID, actor, "fusion-token-created", "fusion-token", tok.ID, detail))), "fusion-token-created"); err != nil {
		return "", store.FusionToken{}, err
	}
	return secret, tok, nil
}

func scopeWords(v []string) string {
	if len(v) == 0 {
		return "all"
	}
	return strings.Join(v, ", ")
}

// ListFusionTokens lists this organisation's tokens, never a secret.
func (c *Service) ListFusionTokens(ctx context.Context) ([]store.FusionToken, error) {
	return c.Store.ListFusionTokens(ctx, c.OrgID)
}

// DeleteFusionToken revokes a token; it stops working at once.
func (c *Service) DeleteFusionToken(ctx context.Context, actor, id string) error {
	toks, err := c.Store.ListFusionTokens(ctx, c.OrgID)
	if err != nil {
		return err
	}
	var name string
	found := false
	for _, t := range toks {
		if t.ID == id {
			name, found = t.Name, true
		}
	}
	if !found {
		return kernel.Errf(kernel.KindNotFound, "no such token")
	}
	return c.AuditedWrite(c.Store.DeleteFusionToken(ctx, c.OrgID, id,
		store.WithAudit(c.AuditRow(c.OrgID, actor, "fusion-token-revoked", "fusion-token", id, name))), "fusion-token-revoked")
}

// AuthenticateFusionToken resolves a presented secret. A malformed, unknown or expired one is the same refusal.
func (c *Service) AuthenticateFusionToken(ctx context.Context, secret string) (store.FusionToken, error) {
	deny := kernel.Errf(kernel.KindUnauthenticated, "sign in required")
	if !kernel.LooksLikeFusionToken(secret) {
		return store.FusionToken{}, deny
	}
	h := kernel.HashSecret(secret)
	tok, err := c.Store.LookupFusionToken(ctx, h)
	if err != nil {
		return store.FusionToken{}, deny
	}
	now := c.Now()
	if !now.Before(tok.ExpiresAt) {
		return store.FusionToken{}, deny
	}
	if tok.LastUsed == nil || now.Sub(*tok.LastUsed) > accounts.TouchEvery {
		_ = c.Store.TouchFusionToken(ctx, h, now)
	}
	return tok, nil
}

type TokenDoc struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Signals    []fusionapi.Signal `json:"signals"`
	Namespaces []string           `json:"namespaces"`
	Clusters   []string           `json:"clusters"`
	CreatedBy  string             `json:"createdBy"`
	CreatedAt  string             `json:"createdAt"`
	ExpiresAt  string             `json:"expiresAt"`
	LastUsedAt string             `json:"lastUsedAt,omitempty"`
}

func toTokenDoc(t store.FusionToken) TokenDoc {
	return TokenDoc{ID: t.ID, Name: t.Name, Signals: fusionapi.SignalsOf(t.Signals), Namespaces: orEmpty(t.Namespaces), Clusters: orEmpty(t.Clusters),
		CreatedBy: t.CreatedBy, CreatedAt: kernel.RFC(t.CreatedAt), ExpiresAt: kernel.RFC(t.ExpiresAt), LastUsedAt: kernel.RFCP(t.LastUsed)}
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func signalStrings(v []fusionapi.Signal) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = string(s)
	}
	return out
}
