package fusion

// CreateTokenRequest is the body of POST .../fusion/tokens: what the token may read, and for how long (ExpiresInDays 0 is the default).
type CreateTokenRequest struct {
	Name          string   `json:"name"`
	Signals       []string `json:"signals"`
	Namespaces    []string `json:"namespaces"`
	Clusters      []string `json:"clusters"`
	ExpiresInDays int      `json:"expiresInDays"`
}

// CreatedTokenDoc is the one answer that ever carries the raw secret: the token, shown once, and what is kept of it.
type CreatedTokenDoc struct {
	Details TokenDoc `json:"details"`
	Token   string   `json:"token"`
}
