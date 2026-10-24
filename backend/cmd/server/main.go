// Command server is the Ikhnos control plane: it enrolls agents, receives what they
// discover, and serves the JSON API and UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
	"ikhnos/internal/fusion"
	"ikhnos/internal/geoip"
	"ikhnos/internal/graph"
	"ikhnos/internal/installcfg"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/passkey"
	"ikhnos/internal/pki"
	"ikhnos/internal/probe"
	"ikhnos/internal/server"
	"ikhnos/internal/store"
)

var version = "0.1.0-dev"

// agentChartVersion is the ikhnos-agent chart version actually published alongside this server build - set at
// link time (-X main.agentChartVersion=...) by the release pipeline, which packages the chart with an explicit
// `helm package --version` that has nothing to do with what's checked into the chart's own Chart.yaml (edge builds
// get 0.0.0-edge.<sha>; a tagged release gets the tag). Left at its zero value, Admin falls back to chart.Version()
// (the Chart.yaml literal), which is only ever right by coincidence - see Admin.agentChartVersion's doc comment.
var agentChartVersion = ""

// operatorChartVersion is agentChartVersion's twin for the ikhnos-regional-operator chart: the release pipeline
// packages every chart of a run under the same version, so the same value is linked in. Without it the regional
// operator install command would name the Chart.yaml literal, which the registry does not hold for an edge build.
var operatorChartVersion = ""

// controllerChartVersion is the same for the optional ikhnos-controller chart, which this binary does not carry: the version the pipeline
// published it under. Unset, the install command for it leaves --version out.
var controllerChartVersion = ""

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-password":
			resetPassword(os.Args[2:])
			return
		case "create-org":
			createOrg(os.Args[2:])
			return
		case "reset-sealing":
			resetSealing(os.Args[2:])
			return
		case "verify-audit":
			os.Exit(verifyAudit(os.Args[2:], os.Stdout, os.Stderr))
		case "backup":
			os.Exit(backupCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "restore":
			os.Exit(restoreCmd(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	probeListen := flag.String("probe-listen", os.Getenv("IKHNOS_PROBE_LISTEN"), "address to listen on for the reports of the node probes the server's chart runs, e.g. :8083 (empty: no node probes). Plain HTTP inside the cluster; every report is signed with the secret in --probe-secret-file. env IKHNOS_PROBE_LISTEN")
	probeSecretFile := flag.String("probe-secret-file", os.Getenv("IKHNOS_PROBE_SECRET_FILE"), "file holding the secret shared with those node probes (at least 16 characters). env IKHNOS_PROBE_SECRET_FILE")
	probeEvery := flag.Duration("probe-interval", 3*time.Minute, "how often those node probes were told to report (the chart's nodeProbe.interval); one silent for three of these is no longer believed")
	var origins multi
	dataDir := flag.String("data-dir", "./data", "where the database and CA live")
	agentListen := flag.String("agent-listen", ":8443", "address the agents connect to (mTLS)")
	agentAddr := flag.String("agent-address", "", "host:port agents use to reach this server, shown in install commands (required)")
	agentExposure := flag.String("agent-exposure", "", "how --agent-address is exposed: loadbalancer, nodeport, gateway or clusterip. Purely informational - it only changes what Settings → Installation suggests when that address needs to change; the server does not validate it against how the port is actually reachable")
	releaseName := flag.String("release-name", "", "this Helm release's own name, so Settings → Installation can print an exact helm upgrade command for changing --agent-address later instead of a fill-in-the-blank one. Optional; set by the chart")
	fusionName := flag.String("fusion-name", "", "the object-name prefix of the FUSION bundled in this Helm release (what the chart calls fusion.name); set by the chart when this server may switch it. Empty: no FUSION switch")
	fusionCentralAddress := flag.String("fusion-central-address", "", "host:port other clusters dial to reach FUSION's central operator (an Ingress, LoadBalancer or NodePort you expose); empty: reachable inside this cluster only")
	releaseNamespace := flag.String("release-namespace", "", "this Helm release's own namespace, for the same reason as --release-name. Optional; set by the chart")
	extraHosts := flag.String("agent-hosts", "", "extra DNS names or IPs for the server certificate, comma separated")
	adminListen := flag.String("admin-listen", "127.0.0.1:8080", "address of the UI and JSON API")
	adminCert := flag.String("admin-tls-cert", "", "TLS certificate for the admin listener (needed when it is not on loopback)")
	adminKey := flag.String("admin-tls-key", "", "TLS key for the admin listener")
	behindProxy := flag.Bool("admin-behind-tls-proxy", false, "serve plain HTTP on a non-loopback address because a TLS-terminating proxy in front protects it. With it, the client address (used for rate limits and the audit trail) is the LAST entry of X-Forwarded-For, the address your proxy itself saw, and X-Forwarded-Proto: https marks the session cookie Secure and enables HSTS. Only set it when the proxy is the sole way to reach this port and overwrites those headers; otherwise a client can forge them")
	ssoHeader := flag.String("admin-sso-header", os.Getenv("IKHNOS_ADMIN_SSO_HEADER"), "name of a request header a trusted reverse proxy sets to a signed-in person's username (e.g. X-Remote-User), after verifying who they are itself (an OAuth2 proxy, an Envoy/Istio ext_authz filter, an IdP-integrated gateway). When set, GET /api/v1/auth/sso signs that username straight in - no password, no second factor - if it already names an existing, enabled account; it never creates or changes one. Requires --admin-behind-tls-proxy, because trusting this header is only safe when that proxy is the sole way to reach this port and always overwrites it, for every request; otherwise anyone who can reach this port directly could set it themselves and sign in as anyone. Empty (default) disables the endpoint entirely; env IKHNOS_ADMIN_SSO_HEADER")
	agentBehindProxy := flag.Bool("agent-behind-proxy", os.Getenv("IKHNOS_AGENT_BEHIND_PROXY") == "true", "an L4 load balancer or reverse proxy sits in front of --agent-listen and is configured to send a PROXY protocol header (v1 or v2) ahead of each connection - the way to preserve the real client address through a TCP passthrough, since the agent's own mTLS handshake rules out a TLS-terminating HTTP proxy here. With it, every connection must carry that header (one that doesn't is refused) and its declared address - not the proxy's own - is what approval cards, rate limits, the audit trail and an agent's suggested location use. Only set it when the proxy is the sole way to reach this port and is actually configured to send the header; env IKHNOS_AGENT_BEHIND_PROXY=true")
	deciderAllow := flag.String("decider-allow-cidrs", os.Getenv("IKHNOS_DECIDER_ALLOW_CIDRS"), "comma-separated CIDRs (10.0.0.0/8,127.0.0.1/32) the server may call for the external decider although they are private or loopback. By default only public addresses are allowed, so a decider address cannot be used to reach internal services. Cloud metadata and link-local addresses are never allowed; env IKHNOS_DECIDER_ALLOW_CIDRS")
	caPassFile := flag.String("ca-key-passphrase-file", os.Getenv("IKHNOS_CA_KEY_PASSPHRASE_FILE"), "file holding a passphrase (at least 12 characters) that encrypts the CA private key at rest (argon2id + AES-256-GCM). A plaintext key is encrypted the first time this is given; an encrypted key without it stops the server. Never give the passphrase itself as a flag value; env IKHNOS_CA_KEY_PASSPHRASE_FILE. Without it the key is stored unencrypted (mode 0600) and a warning is logged. See internal/pki/ROTATION.md")
	caAutoPass := flag.Bool("ca-key-auto-passphrase", os.Getenv("IKHNOS_CA_KEY_AUTO_PASSPHRASE") == "true", "generate the CA key passphrase ourselves with crypto/rand on first start and persist it at <data-dir>/pki/ca.passphrase (mode 0600), reused on every restart, instead of requiring --ca-key-passphrase-file. Mutually exclusive with it. This is what the ikhnos-server chart passes by default (pki.encryptAtRest, with no pki.caKeyPassphraseSecret.name given) now that it no longer generates the passphrase itself in the chart template; give --ca-key-passphrase-file instead if you want the passphrase kept apart from the data directory. env IKHNOS_CA_KEY_AUTO_PASSPHRASE=true")
	sealKeyFile := flag.String("sealing-key-file", os.Getenv("IKHNOS_SEALING_KEY_FILE"), "file holding the key that seals the secrets this server must read back: the second-factor secrets, the mail password and an organisation's decider secret (AES-256-GCM). One key per line, at least 32 characters (openssl rand -base64 48); the first seals, the others only open, so a key is replaced by putting the new one on top and removing the old one later. Without it those secrets are stored as they are and a warning is logged. Once any secret is sealed, the server refuses to start without the key that sealed it. Keep the key apart from the data directory and its backups; env IKHNOS_SEALING_KEY_FILE")
	looseOK := flag.Bool("allow-loose-permissions", os.Getenv("IKHNOS_ALLOW_LOOSE_PERMISSIONS") == "true", "start although the data directory, the CA key or the database is readable or writable by group or other users (by default the server refuses and prints the chmod to run). For platforms that set modes themselves, such as a Kubernetes fsGroup volume only this pod can reach; env IKHNOS_ALLOW_LOOSE_PERMISSIONS=true")
	uiDir := flag.String("ui-dir", "", "built UI to serve (dist/)")
	chartRef := flag.String("chart-ref", "", "Helm chart to install, as shown in install commands (an OCI, repository or https reference). Empty: oci://<image-registry>/ikhnos-agent. \"local\": the copy of the chart this server serves, which the wizard offers as a download")
	imageRegistry := flag.String("image-registry", os.Getenv("IKHNOS_IMAGE_REGISTRY"), "registry and path that hold the agent image and chart you published (registry.example.com/team, or a bare Docker Hub name; see scripts/publish.sh). Install commands then point the chart at <registry>/ikhnos and fetch the chart itself from the same place as an OCI artifact. There is no built-in default: with none, the command uses the chart's own image name (ikhnos/ikhnos) and the chart file this server serves. This is the server-wide default; an organisation's Settings → Installation overrides it; env IKHNOS_IMAGE_REGISTRY")
	imageTag := flag.String("image-tag", os.Getenv("IKHNOS_IMAGE_TAG"), "image tag for install commands; empty means the chart's own appVersion. Needs --image-registry; env IKHNOS_IMAGE_TAG")
	imageDigest := flag.String("image-digest", os.Getenv("IKHNOS_IMAGE_DIGEST"), "image digest (sha256:<64 hex>, printed by scripts/publish.sh) to pin install commands to, which makes them immutable and reproducible; the tag is then ignored by the chart. Needs --image-registry; env IKHNOS_IMAGE_DIGEST")
	org := flag.String("org", "default", "id of the organization created together with the first account, on a fresh database")
	registration := flag.String("registration", os.Getenv("IKHNOS_REGISTRATION"), "who may create an account: open (anyone, and they get an organization of their own), invite (only with an invitation from an organization) or closed (nobody; use the create-org and reset-password commands); env IKHNOS_REGISTRATION. Default: open while --admin-listen is a loopback address (local use), invite otherwise")
	multiOrg := flag.Bool("multi-organisation", os.Getenv("IKHNOS_MULTI_ORGANISATION") == "true", "let this server host more than one organisation: people can create further ones (where registration is open) and open registration gives every newcomer an organisation of their own. Without it the server is one organisation's: the one created with the first account, joined by invitation. Organisations that already exist go on working either way; env IKHNOS_MULTI_ORGANISATION=true")
	geoDB := flag.String("geoip-db", os.Getenv("IKHNOS_GEOIP_DB"), "optional MaxMind-format .mmdb (DB-IP Lite, GeoLite2) used offline to suggest a location for each agent's connecting address; env IKHNOS_GEOIP_DB")
	geoPublicIP := flag.String("geoip-public-ip-service", os.Getenv("IKHNOS_GEOIP_PUBLIC_IP_SERVICE"), "needs --geoip-db. An http(s) URL that answers with this server's own public IP as plain text (a well known one: https://api.ipify.org). Used only as a fallback: when an agent's connecting address cannot be located at all (private, loopback, CGNAT - agent and server sharing a network is exactly why it looks that way), this server's own public address is the closest honest guess, looked up and returned marked estimated rather than exact. Called once at startup and then on an hourly cache, never per request; empty (default) never guesses, only an agent's own address is ever used. Nothing about your topology, applications or data goes out in the request - just an empty GET, to a URL you chose; env IKHNOS_GEOIP_PUBLIC_IP_SERVICE")
	geoASNDB := flag.String("geoip-asn-db", os.Getenv("IKHNOS_GEOIP_ASN_DB"), "needs --geoip-db. A second, optional MaxMind-format .mmdb in ASN shape (GeoLite2-ASN, DB-IP ASN Lite) rather than city/country shape. Whenever --geoip-db places an address (including an --geoip-public-ip-service estimate), this adds which network it belongs to - its AS number and the organisation that announces it, e.g. 15169 / \"Google LLC\" - a steadier signal than city or country, and unaffected by a VPN or cloud egress moving the place shown. Shown only alongside a location, never on its own: an address --geoip-db has no record for stays unanswered even when this database recognises it. Entirely offline, same as --geoip-db: nothing is looked up over the network; env IKHNOS_GEOIP_ASN_DB")
	neoURL := flag.String("neo4j-url", os.Getenv("IKHNOS_NEO4J_URL"), "Neo4j HTTP address (http://host:7474). When set, topology history, events, audit and workspace revisions are kept in a Neo4j graph; env IKHNOS_NEO4J_URL")
	neoUser := flag.String("neo4j-user", envOr("IKHNOS_NEO4J_USER", "neo4j"), "Neo4j user; env IKHNOS_NEO4J_USER")
	neoDB := flag.String("neo4j-database", envOr("IKHNOS_NEO4J_DATABASE", "neo4j"), "Neo4j database name; env IKHNOS_NEO4J_DATABASE")
	neoPassFile := flag.String("neo4j-password-file", os.Getenv("IKHNOS_NEO4J_PASSWORD_FILE"), "file holding the Neo4j password (or set IKHNOS_NEO4J_PASSWORD); env IKHNOS_NEO4J_PASSWORD_FILE")
	neoInsecure := flag.Bool("neo4j-allow-insecure-http", os.Getenv("IKHNOS_NEO4J_ALLOW_INSECURE_HTTP") == "true", "allow the Neo4j password and the topology to be sent over plain HTTP to a host that is not loopback. By default the server refuses to start: use an https:// address, or, on a network you trust (a pod-to-pod link inside one cluster, say), pass this flag; env IKHNOS_NEO4J_ALLOW_INSECURE_HTTP=true")
	smtpHost := flag.String("smtp-host", os.Getenv("IKHNOS_SMTP_HOST"), "SMTP server for outgoing mail (a login or email-verification code - nothing else is ever sent), and the default until an owner of the default organisation sets one in Settings instead. Without either, email is simply not offered as a second factor; env IKHNOS_SMTP_HOST")
	smtpPort := flag.String("smtp-port", envOr("IKHNOS_SMTP_PORT", "587"), "SMTP submission port. STARTTLS is used automatically when the server offers it; env IKHNOS_SMTP_PORT")
	smtpUser := flag.String("smtp-username", os.Getenv("IKHNOS_SMTP_USERNAME"), "SMTP username, if the relay requires authentication; env IKHNOS_SMTP_USERNAME")
	smtpPassFile := flag.String("smtp-password-file", os.Getenv("IKHNOS_SMTP_PASSWORD_FILE"), "file holding the SMTP password (or set IKHNOS_SMTP_PASSWORD); never given as a flag value, which would show in the process list; env IKHNOS_SMTP_PASSWORD_FILE")
	smtpFrom := flag.String("smtp-from", os.Getenv("IKHNOS_SMTP_FROM"), "From: address on mailed codes. Required with --smtp-host; env IKHNOS_SMTP_FROM")
	flag.Var(&origins, "allow-origin", "browser origin allowed to call the API cross-origin (repeatable, development)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	applyIdentityPolicy(log, *requireIdentityURI)
	if *agentAddr == "" {
		fatal(log, errors.New("--agent-address is required, for example ikhnos.example.com:8443"))
	}
	regMode, regWhy := resolveRegistration(*registration, *adminListen)
	switch regMode {
	case accounts.RegOpen, accounts.RegInvite, accounts.RegClosed:
	default:
		fatal(log, fmt.Errorf("--registration must be open, invite or closed, not %q", regMode))
	}
	log.Info("account registration", "mode", regMode, "why", regWhy)
	if !*multiOrg && regMode == accounts.RegOpen {
		log.Info("this server hosts a single organisation, so open registration is by invitation (pass --multi-organisation to let people create organisations)")
	}
	if regMode == accounts.RegOpen && !isLoopback(*adminListen) {
		log.Warn("registration is open on a non-loopback address: anyone who can reach the admin address can create an account and an organization. Fine for a private network; on an exposed server use --registration invite", "admin", *adminListen)
	}
	decider, err := kernel.NewDeciderPolicy(*deciderAllow)
	if err != nil {
		fatal(log, err)
	}
	if a := decider.Allowed(); len(a) > 0 {
		log.Warn("the external decider may be a private address in these ranges (--decider-allow-cidrs); cloud metadata and link-local addresses stay blocked", "ranges", a)
	}
	var neo *graph.Config
	if *neoURL != "" {
		pw := os.Getenv("IKHNOS_NEO4J_PASSWORD")
		if *neoPassFile != "" {
			b, err := os.ReadFile(*neoPassFile)
			if err != nil {
				fatal(log, fmt.Errorf("--neo4j-password-file: %w", err))
			}
			pw = strings.TrimSpace(string(b))
		}
		if pw == "" {
			fatal(log, errors.New("--neo4j-url needs a password: set IKHNOS_NEO4J_PASSWORD or --neo4j-password-file (it is not accepted as a flag value, which would show in the process list)"))
		}
		neo = &graph.Config{URL: *neoURL, User: *neoUser, Password: pw, Database: *neoDB}
		if err := checkNeo4jTransport(log, *neoURL, *neoInsecure); err != nil {
			fatal(log, err)
		}
	}
	// There is no default registry, and "none" no longer means "off" (leave the flag empty), so refuse it rather than
	// print commands that pull from a Docker Hub namespace called "none".
	if *imageRegistry == "none" {
		fatal(log, errors.New("--image-registry: \"none\" is no longer a value; leave it empty for the chart's own image names"))
	}
	img, err := kernel.ImageConfig{Registry: *imageRegistry, Tag: *imageTag, Digest: *imageDigest}.Normalize()
	if err != nil {
		fatal(log, fmt.Errorf("--image-registry/--image-tag/--image-digest: %w", err))
	}
	var mail accounts.MailConfig
	if *smtpHost != "" {
		if *smtpFrom == "" {
			fatal(log, errors.New("--smtp-host needs --smtp-from"))
		}
		pw := os.Getenv("IKHNOS_SMTP_PASSWORD")
		if *smtpPassFile != "" {
			b, err := os.ReadFile(*smtpPassFile)
			if err != nil {
				fatal(log, fmt.Errorf("--smtp-password-file: %w", err))
			}
			pw = strings.TrimSpace(string(b))
		}
		mail = accounts.MailConfig{Host: *smtpHost, Port: *smtpPort, Username: *smtpUser, Password: pw, From: *smtpFrom}
	}
	if (*probeListen == "") != (*probeSecretFile == "") {
		fatal(log, errors.New("--probe-listen and --probe-secret-file must be given together"))
	}
	if err := run(log, probeOpts{Listen: *probeListen, SecretFile: *probeSecretFile, Every: *probeEvery}, *dataDir, *agentListen, *agentAddr, *agentExposure, *releaseName, *releaseNamespace, *fusionName, *fusionCentralAddress, *extraHosts, *adminListen, *adminCert, *adminKey, *behindProxy, *agentBehindProxy, *multiOrg, *ssoHeader, *uiDir, *chartRef, img, *org, regMode, *geoDB, *geoPublicIP, *geoASNDB, decider, neo, mail, keyOpts{PassphraseFile: *caPassFile, AutoPassphrase: *caAutoPass, AllowLoose: *looseOK, SealingKeyFile: *sealKeyFile}, origins); err != nil {
		fatal(log, err)
	}
}

// keyOpts is how the operator asked for the CA key and the data directory to be protected.
// probeOpts is where the server hears from the node probes its own chart runs (see server/self_probe.go).
type probeOpts struct {
	Listen, SecretFile string
	Every              time.Duration
}

// serveSelfProbe starts the receiver for those probes. Nil, nil when the server has none. The secret is checked here, so a
// server that is told to listen refuses to start with one it could not verify anything with.
func serveSelfProbe(log *slog.Logger, o probeOpts) (*probe.Receiver, *http.Server, error) {
	if o.Listen == "" {
		return nil, nil, nil
	}
	b, err := os.ReadFile(o.SecretFile)
	secret := []byte(strings.TrimSpace(string(b)))
	if err != nil || len(secret) < 16 {
		return nil, nil, fmt.Errorf("--probe-secret-file must hold a secret of at least 16 characters: %v", err)
	}
	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("--probe-listen: %w", err)
	}
	rcv := probe.NewReceiver(secret, log)
	srv := &http.Server{Handler: rcv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("node probe receiver stopped", "err", err)
		}
	}()
	log.Info("listening for node probe reports", "addr", ln.Addr().String())
	return rcv, srv, nil
}

type keyOpts struct {
	PassphraseFile string
	AutoPassphrase bool
	AllowLoose     bool
	SealingKeyFile string
}

// resolveCAPassphrase decides what (if anything) protects the CA key at rest, from how the operator
// asked for it via keyOpts: their own passphrase file, this process minting and persisting one itself
// (see pki.AutoPassphrase), or neither (an unencrypted key - pki.LoadOrCreateWith logs the warning).
func resolveCAPassphrase(log *slog.Logger, keys keyOpts, dataDir string) ([]byte, error) {
	switch {
	case keys.PassphraseFile != "" && keys.AutoPassphrase:
		return nil, errors.New("--ca-key-passphrase-file and --ca-key-auto-passphrase are mutually exclusive: give one or the other")
	case keys.PassphraseFile != "":
		return readPassphraseFile(log, keys.PassphraseFile)
	case keys.AutoPassphrase:
		return pki.AutoPassphrase(filepath.Join(dataDir, "pki"))
	default:
		return nil, nil
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(log *slog.Logger, err error) {
	log.Error("fatal", "err", err)
	os.Exit(1)
}

func run(log *slog.Logger, selfProbe probeOpts, dataDir, agentListen, agentAddr, agentExposure, releaseName, releaseNamespace, fusionName, fusionCentralAddress, extraHosts, adminListen, adminCert, adminKey string, behindProxy, agentBehindProxy, multiOrg bool, ssoHeader, uiDir, chartRef string, img kernel.ImageConfig, org, registration, geoPath, geoPublicIP, geoASNPath string, decider *kernel.DeciderPolicy, neo *graph.Config, mail accounts.MailConfig, keys keyOpts, origins []string) error {
	if err := checkSSOHeader(ssoHeader, behindProxy); err != nil {
		return err
	}
	// Fail closed: a database that was asked for but cannot be used stops the server rather than silently turning the feature off.
	var geo *agents.Geo
	if geoPath != "" {
		db, err := geoip.Open(geoPath)
		if err != nil {
			return fmt.Errorf("--geoip-db %s: %w", geoPath, err)
		}
		geo = agents.NewGeo(db)
		gi := geo.Info()
		log.Info("geoip database loaded", "type", gi.Database, "description", gi.Description, "built", gi.BuiltAt)
		if geoASNPath != "" {
			asnDB, err := geoip.Open(geoASNPath)
			if err != nil {
				return fmt.Errorf("--geoip-asn-db %s: %w", geoASNPath, err)
			}
			geo.SetASN(asnDB)
			ai := asnDB.Info()
			log.Info("geoip ASN database loaded: locations will also carry which network the address belongs to", "type", ai.DatabaseType, "description", ai.Description)
		}
	} else {
		if geoPublicIP != "" {
			return errors.New("--geoip-public-ip-service needs --geoip-db (it is only ever a fallback for addresses the database would otherwise place)")
		}
		if geoASNPath != "" {
			return errors.New("--geoip-asn-db needs --geoip-db (it only ever adds to a location that database found)")
		}
	}
	if err := ensurePrivateDir(dataDir); err != nil {
		return err
	}
	if err := checkPermissions(log, dataDir, keys.AllowLoose); err != nil {
		return err
	}
	passphrase, err := resolveCAPassphrase(log, keys, dataDir)
	if err != nil {
		return err
	}
	ca, err := pki.LoadOrCreateWith(filepath.Join(dataDir, "pki"), pki.Options{Passphrase: passphrase, Log: log})
	if err != nil {
		return err
	}
	sealer, err := loadSealer(log, keys.SealingKeyFile)
	if err != nil {
		return err
	}
	sqlite, err := store.OpenSQLite(filepath.Join(dataDir, "ikhnos.db"))
	if err != nil {
		return err
	}
	// A database that holds sealed secrets stops here without their key, or with another one, and not at the first login.
	if err := sqlite.UseSealer(context.Background(), sealer); err != nil {
		sqlite.Close()
		return err
	}
	if sealer.Enabled() {
		log.Info("secrets are sealed at rest", "key", sealer.KeyID(), "keys", len(sealer.KeyIDs()))
	} else {
		log.Warn("secrets are stored as they are: the second-factor secrets, the mail password and the deciders' secrets are readable to anyone who has the database file; give --sealing-key-file to seal them")
	}
	// The database's -wal and -shm files exist only now.
	if err := checkPermissions(log, dataDir, keys.AllowLoose); err != nil {
		sqlite.Close()
		return err
	}
	defer sqlite.Close()
	var st store.Store = sqlite
	var graphStore *graph.Store
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	if geo != nil && geoPublicIP != "" {
		finder := agents.NewPublicIPFinder(geoPublicIP, 0)
		finder.Start(runCtx)
		geo.SetPublicIPFallback(finder)
		log.Info("an unlocatable agent (private, loopback or CGNAT address) is estimated from this server's own public address", "service", geoPublicIP)
	}
	if neo != nil {
		client, err := graph.NewClient(*neo)
		if err != nil {
			return err
		}
		gs := graph.Wrap(sqlite, graph.NewDB(client), log)
		gs.Start(runCtx)
		st, graphStore = gs, gs
		log.Info("history, events, audit and workspace revisions go to Neo4j", "url", neo.URL, "database", neo.Database)
	}

	host, _, err := net.SplitHostPort(agentAddr)
	if err != nil {
		return fmt.Errorf("--agent-address must be host:port: %w", err)
	}
	hosts := []string{host}
	for _, h := range strings.Split(extraHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	certs := pki.NewServerCerts(ca, hosts)

	// The platform-wide core has no organization of its own: accounts, sign-in and the calls that arrive
	// before an agent is known belong to it, and each organization gets a scoped view of it.
	core := server.NewCore(st, ca, "", log)
	core.DefaultOrg, core.RegMode, core.Decider = org, registration, decider
	core.Sealer = sealer
	core.SingleOrg = !multiOrg
	// mail is the --smtp-* flags' value: the boot-time default, in effect until an owner of the default
	// organisation saves one through Settings, and again after that if the database is ever reset without
	// the flags changing to match (see SetMailerDefault, LoadMailConfig).
	core.Accounts.SetMailerDefault(mail)
	core.Accounts.LoadMailConfig(context.Background())
	core.PendingTTL = *pendingTTL
	core.TrustAgentProxy = agentBehindProxy
	// Unlike Mailer, passkeys need no operator configuration - go-webauthn works out of the relying party
	// info Admin derives from each request's own Host header, so the provider is simply always on.
	core.Accounts.SetWebAuthn(passkey.New())
	created, firstPassword, err := core.Accounts.BootstrapAdmin(context.Background(), os.Getenv("IKHNOS_ADMIN_PASSWORD"))
	if err != nil {
		return err
	}
	platform := server.NewPlatform(core, geo)
	if err := platform.Start(runCtx); err != nil {
		return err
	}
	// Every document that holds a secret has been read, and sealed if it was in the clear: now the file is rewritten so the old text is not left in it.
	if err := sqlite.ScrubPlaintext(context.Background()); err != nil {
		return err
	}
	grpcSrv := core.Agents.NewGRPC(certs, platform)

	l, err := net.Listen("tcp", agentListen)
	if err != nil {
		return err
	}
	fatal := make(chan error, 2) // a listener that stops on its own ends the server: it cannot do its work without one
	go func() {
		if err := grpcSrv.Serve(l); err != nil {
			log.Error("agent listener stopped", "err", err)
			fatal <- fmt.Errorf("the agent listener stopped: %w", err)
		}
	}()

	admin := &server.Admin{P: platform, C: core, TrustProxy: behindProxy, SSOHeaderName: ssoHeader, SecureCookies: !isLoopback(adminListen), Origins: origins, UIDir: uiDir, Version: version,
		Settings: installcfg.Settings{AgentAddr: agentAddr, AgentExposure: agentExposure, ReleaseName: releaseName, ReleaseNamespace: releaseNamespace, ChartRef: chartRef,
			ImageRegistry: img.Registry, ImageTag: img.Tag, ImageDigest: img.Digest, PublicURL: os.Getenv("IKHNOS_PUBLIC_URL"),
			AgentChartVersion: agentChartVersion, OperatorChartVersion: operatorChartVersion, ControllerChartVersion: controllerChartVersion}}
	if err := admin.CheckInstallConfig(); err != nil {
		return err
	}
	// The private CA that signed this server's own HTTPS certificate, when it has one: handed to a regional operator so its
	// heartbeat can verify the server. A file the administrator mounts (extraVolumes); unreadable is a startup error, not a
	// silent heartbeat that can never connect.
	if f := os.Getenv("IKHNOS_HEARTBEAT_CA_FILE"); f != "" {
		pem, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read IKHNOS_HEARTBEAT_CA_FILE: %w", err)
		}
		admin.HeartbeatCAPEM = pem
	}
	if fusionName != "" {
		admin.Fusion = fusionControl(log, fusionName, releaseNamespace, fusionCentralAddress, core.Accounts.MainOrg())
		// An address set from the screen (Reachable at on the central operator) outranks the startup flag, which is
		// only the first value.
		if op, err := core.Store.GetOperator(context.Background(), core.Accounts.MainOrg(), operators.CentralOperatorID); err == nil && op.Address != "" {
			admin.Fusion.SetPublicAddress(op.Address)
		}
		// The renewal has no signed-in user to take an organisation from, and the platform core has none: FUSION's
		// central operator lives in the main organisation, so it runs on that organisation's view of the core.
		go renewFusionCertificate(log, admin.Fusion, admin.Fusion.OrgCore(core.Fusion))
		// Telemetry does not carry which Ikhnos application a service is in, so the tools that read Prometheus directly
		// are given it as a series of its own.
		go admin.RunApplicationInfo(context.Background(), log)
	}
	probes, probeSrv, err := serveSelfProbe(log, selfProbe)
	if err != nil {
		return err
	}
	admin.SelfProbe, admin.SelfProbeEvery = probes, selfProbe.Every
	admin.Readiness = &server.Readiness{AgentsListening: grpcSrv.Serving}
	if graphStore != nil {
		admin.Readiness.Graph = func() (bool, bool) { return true, graphStore.Ready() }
	}
	metricsSrv, err := serveMetrics(log, platform, version)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Addr: adminListen, Handler: admin.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	tlsOn := adminCert != "" && adminKey != ""
	if (adminCert == "") != (adminKey == "") {
		return errors.New("--admin-tls-cert and --admin-tls-key must be given together")
	}
	if !tlsOn && !isLoopback(adminListen) {
		if !behindProxy {
			return fmt.Errorf("refusing to serve the admin API in clear text on %s: sign-in passwords and the session cookie would cross the network. Give --admin-tls-cert and --admin-tls-key, or pass --admin-behind-tls-proxy if a TLS-terminating proxy protects this port", adminListen)
		}
		log.Warn("admin API is plain HTTP on a non-loopback address; this is safe only because a TLS proxy in front terminates HTTPS", "listen", adminListen)
	}
	go func() {
		var err error
		if tlsOn {
			err = httpSrv.ListenAndServeTLS(adminCert, adminKey)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("admin listener stopped", "err", err)
			fatal <- fmt.Errorf("the admin listener stopped: %w", err)
		}
	}()

	log.Info("ikhnos server started", "agents", agentListen, "admin", adminListen, "registration", registration, "ca_pin", ca.Pin(), "ca_spki_pin", ca.SPKIPin())
	if created && firstPassword != "" {
		// Shown once, never stored in readable form. It must be replaced at first sign-in.
		fmt.Fprintf(os.Stderr, "\n  First start: sign in as  admin  with the one-time password  %s\n  You will be asked to choose a new password straight away.\n\n", firstPassword)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "admin-token")); err == nil {
		log.Warn("the shared admin token is no longer used; sign in with a user account. You can delete this file", "file", filepath.Join(dataDir, "admin-token"))
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	endErr := waitForEnd(sig, fatal)
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	if probeSrv != nil {
		_ = probeSrv.Shutdown(ctx)
	}
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(ctx)
	}
	// Agents' streams never end on their own, so a graceful stop would wait for them forever: give them a few seconds, then close.
	grpcSrv.StopWithin(5 * time.Second)
	// Nothing is writing any more: save what the timers had not yet, while the database is still open.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	platform.FlushAll(flushCtx)
	return endErr
}

// waitForEnd blocks until the server is asked to stop (nil) or a listener it cannot work without stopped on its own (that error,
// so the process exits with a failure and whatever supervises it starts it again instead of leaving it running and unreachable).
func waitForEnd(sig <-chan os.Signal, fatal <-chan error) error {
	select {
	case <-sig:
		return nil
	case err := <-fatal:
		return err
	}
}

// resolveRegistration picks the registration mode. An explicit flag or environment value always wins. Without
// one, a server that listens only on loopback (local use) keeps registration open so that it just works, and
// any other listener defaults to invitation-only: an exposed server must not let strangers create accounts
// unless the operator says so. why is what the startup log tells the operator.
func resolveRegistration(explicit, adminListen string) (mode, why string) {
	if explicit != "" {
		return explicit, "set explicitly by --registration or IKHNOS_REGISTRATION"
	}
	if isLoopback(adminListen) {
		return accounts.RegOpen, "default: the admin listener " + adminListen + " is loopback, so only this machine can reach it"
	}
	return accounts.RegInvite, "default: the admin listener " + adminListen + " is not loopback, so accounts are by invitation only (pass --registration open to allow anyone to sign up)"
}

// verifyAudit checks the audit trail's hash chain and reports the first broken link. Exit status: 0 intact,
// 1 broken, 2 could not be checked.
func verifyAudit(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("verify-audit", flag.ContinueOnError)
	fs.SetOutput(errw)
	dataDir := fs.String("data-dir", "./data", "the server's data directory")
	fs.Usage = func() { fmt.Fprintln(errw, "usage: server verify-audit [--data-dir DIR]") }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	path := filepath.Join(*dataDir, "ikhnos.db")
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(errw, "verify-audit: %v\n", err)
		return 2
	}
	st, err := store.OpenSQLite(path)
	if err != nil {
		fmt.Fprintf(errw, "verify-audit: %v\n", err)
		return 2
	}
	defer st.Close()
	r, err := st.VerifyAudit(context.Background())
	if err != nil {
		fmt.Fprintf(errw, "verify-audit: %v\n", err)
		return 2
	}
	fmt.Fprintf(out, "audit rows: %d (%d written before the hash chain existed and not covered, %d chained and verified)\n", r.Rows, r.Unchained, r.Chained)
	if r.Head != "" {
		fmt.Fprintf(out, "chain head: %s\n(record this value somewhere the server cannot write to; a later check that finds a different chain for the same rows shows a rollback or rewrite)\n", r.Head)
	}
	if !r.OK {
		fmt.Fprintf(out, "BROKEN at audit row %d: %s\n", r.BrokenAt, r.Problem)
		return 1
	}
	fmt.Fprintln(out, "OK: every link in the chain holds.")
	return 0
}

// checkSSOHeader refuses trusted-header SSO unless the operator also said a TLS-terminating proxy is the
// sole way to reach the admin port: trusting a header for identity is a much bigger step than trusting one
// for the client address, and is only safe under the same "proxy is the only door in" condition.
func checkSSOHeader(ssoHeader string, behindProxy bool) error {
	if ssoHeader != "" && !behindProxy {
		return errors.New("--admin-sso-header requires --admin-behind-tls-proxy: trusting a proxy-asserted identity header is only safe when that same proxy is the sole way to reach this port")
	}
	return nil
}

// checkNeo4jTransport refuses to send the Neo4j credentials, and the topology, in clear text to another
// machine unless the operator said that is acceptable. Loopback stays working without a flag.
func checkNeo4jTransport(log *slog.Logger, raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil // graph.NewClient reports a malformed address
	}
	if u.Scheme != "http" || hostIsLoopback(u.Hostname()) {
		return nil
	}
	if !allowInsecure {
		return fmt.Errorf("refusing to send the Neo4j password over plain HTTP to %s: it and the topology would cross the network in clear text. Use an https:// address, or pass --neo4j-allow-insecure-http (env IKHNOS_NEO4J_ALLOW_INSECURE_HTTP=true) if the network between the two is one you trust", u.Host)
	}
	log.Warn("the Neo4j connection is plain HTTP to another host (--neo4j-allow-insecure-http): the password and the topology cross the network in clear text", "host", u.Host)
	return nil
}

func hostIsLoopback(h string) bool {
	ip := net.ParseIP(h)
	return strings.EqualFold(h, "localhost") || (ip != nil && ip.IsLoopback())
}

func isLoopback(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

// resetPassword is the recovery path when nobody can sign in: run it on the host that has the data
// directory. It sets a new random one-time password for the account (creating the account if it does not
// exist, without any organization: see create-org), signs it out everywhere and prints the password once.
func resetPassword(args []string) {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "the server's data directory")
	sealKey := fs.String("sealing-key-file", os.Getenv("IKHNOS_SEALING_KEY_FILE"), "the server's sealing key file, if it has one (a user's second-factor secret cannot be read without it); env IKHNOS_SEALING_KEY_FILE")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: server reset-password [--data-dir DIR] [--sealing-key-file FILE] USERNAME")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := openSealedStore(context.Background(), log, *dataDir, *sealKey)
	if err != nil {
		fatal(log, err)
	}
	defer st.Close()
	rc := server.NewCore(st, nil, "", log)
	rc.Sealer = st.Sealer()
	pw, err := rc.Accounts.RecoverPassword(context.Background(), fs.Arg(0))
	if err != nil {
		fatal(log, err)
	}
	fmt.Printf("One-time password for %s: %s\nIt must be changed at the next sign-in.\n", fs.Arg(0), pw)
}

// createOrg makes an organization owned by an existing account, whatever the registration mode. It is how
// accounts are provisioned on a server with --registration closed.
func createOrg(args []string) {
	fs := flag.NewFlagSet("create-org", flag.ExitOnError)
	dataDir := fs.String("data-dir", "./data", "the server's data directory")
	owner := fs.String("owner", "", "username of the account that will own it (required)")
	sealKey := fs.String("sealing-key-file", os.Getenv("IKHNOS_SEALING_KEY_FILE"), "the server's sealing key file, if it has one; env IKHNOS_SEALING_KEY_FILE")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: server create-org [--data-dir DIR] [--sealing-key-file FILE] --owner USERNAME NAME")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 || *owner == "" {
		fs.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := openSealedStore(context.Background(), log, *dataDir, *sealKey)
	if err != nil {
		fatal(log, err)
	}
	defer st.Close()
	oc := server.NewCore(st, nil, "", log)
	oc.Sealer = st.Sealer()
	o, err := oc.Accounts.CreateOrgFor(context.Background(), *owner, fs.Arg(0))
	if err != nil {
		fatal(log, err)
	}
	fmt.Printf("Created organization %q (%s), owned by %s.\n", o.Name, o.ID, *owner)
}

// renewFusionCertificate keeps the central gateway's certificate fresh: shortly after start (so a changed public
// address is picked up as soon as the server is redeployed) and then daily. Failures are logged, never fatal.
func renewFusionCertificate(log *slog.Logger, f *fusion.FusionControl, core *fusion.Service) {
	for wait := time.Minute; ; wait = 24 * time.Hour {
		time.Sleep(wait)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if err := f.Renew(ctx, core); err != nil {
			log.Warn("could not renew FUSION's gateway certificate", "err", err)
		}
		cancel()
	}
}

// fusionControl builds the FUSION switch for a server whose chart bundles it. Without a usable Kubernetes token (the
// chart's fusionControl.enabled is off, or this is not a cluster) it still returns a switch, with no way to reach the
// API: the screen then says FUSION cannot be switched from here instead of hiding that FUSION exists.
func fusionControl(log *slog.Logger, name, namespace, publicAddress, org string) *fusion.FusionControl {
	f := &fusion.FusionControl{Name: name, Namespace: namespace, PublicAddress: strings.TrimSpace(publicAddress), Org: org}
	// The chart passes the UID of the kube-system namespace it was installed into: the cluster this server, and FUSION, are in.
	f.ClusterUID = strings.TrimSpace(os.Getenv("IKHNOS_CLUSTER_ID"))
	if _, ok := kernel.ClusterIDForUID(org, f.ClusterUID); f.ClusterUID != "" && !ok {
		log.Warn("IKHNOS_CLUSTER_ID is not a namespace UID; ignored", "value", f.ClusterUID)
		f.ClusterUID = ""
	}
	if f.Namespace == "" {
		if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			f.Namespace = strings.TrimSpace(string(b))
		}
	}
	kube, err := kernel.InClusterKube(f.Namespace)
	if err != nil {
		log.Warn("FUSION cannot be switched from this server", "reason", err)
		return f
	}
	f.Kube = kube
	return f
}
