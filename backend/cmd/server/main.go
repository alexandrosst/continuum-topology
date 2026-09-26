// Command server is the Continuum control plane: it enrolls agents, receives what they
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

	"continuum/internal/geoip"
	"continuum/internal/graph"
	"continuum/internal/passkey"
	"continuum/internal/pki"
	"continuum/internal/server"
	"continuum/internal/store"
)

var version = "0.1.0-dev"

// agentChartVersion is the continuum-agent chart version actually published alongside this server build - set at
// link time (-X main.agentChartVersion=...) by the release pipeline, which packages the chart with an explicit
// `helm package --version` that has nothing to do with what's checked into the chart's own Chart.yaml (edge builds
// get 0.0.0-edge.<sha>; a tagged release gets the tag). Left at its zero value, Admin falls back to chart.Version()
// (the Chart.yaml literal), which is only ever right by coincidence - see Admin.agentChartVersion's doc comment.
var agentChartVersion = ""

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
		case "verify-audit":
			os.Exit(verifyAudit(os.Args[2:], os.Stdout, os.Stderr))
		case "backup":
			os.Exit(backupCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "restore":
			os.Exit(restoreCmd(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	var origins multi
	dataDir := flag.String("data-dir", "./data", "where the database and CA live")
	agentListen := flag.String("agent-listen", ":8443", "address the agents connect to (mTLS)")
	agentAddr := flag.String("agent-address", "", "host:port agents use to reach this server, shown in install commands (required)")
	agentExposure := flag.String("agent-exposure", "", "how --agent-address is exposed: loadbalancer, nodeport, gateway or clusterip. Purely informational - it only changes what Settings → Installation suggests when that address needs to change; the server does not validate it against how the port is actually reachable")
	releaseName := flag.String("release-name", "", "this Helm release's own name, so Settings → Installation can print an exact helm upgrade command for changing --agent-address later instead of a fill-in-the-blank one. Optional; set by the chart")
	releaseNamespace := flag.String("release-namespace", "", "this Helm release's own namespace, for the same reason as --release-name. Optional; set by the chart")
	extraHosts := flag.String("agent-hosts", "", "extra DNS names or IPs for the server certificate, comma separated")
	adminListen := flag.String("admin-listen", "127.0.0.1:8080", "address of the UI and JSON API")
	adminCert := flag.String("admin-tls-cert", "", "TLS certificate for the admin listener (needed when it is not on loopback)")
	adminKey := flag.String("admin-tls-key", "", "TLS key for the admin listener")
	behindProxy := flag.Bool("admin-behind-tls-proxy", false, "serve plain HTTP on a non-loopback address because a TLS-terminating proxy in front protects it. With it, the client address (used for rate limits and the audit trail) is the LAST entry of X-Forwarded-For, the address your proxy itself saw, and X-Forwarded-Proto: https marks the session cookie Secure and enables HSTS. Only set it when the proxy is the sole way to reach this port and overwrites those headers; otherwise a client can forge them")
	ssoHeader := flag.String("admin-sso-header", os.Getenv("CONTINUUM_ADMIN_SSO_HEADER"), "name of a request header a trusted reverse proxy sets to a signed-in person's username (e.g. X-Remote-User), after verifying who they are itself (an OAuth2 proxy, an Envoy/Istio ext_authz filter, an IdP-integrated gateway). When set, GET /api/v1/auth/sso signs that username straight in - no password, no second factor - if it already names an existing, enabled account; it never creates or changes one. Requires --admin-behind-tls-proxy, because trusting this header is only safe when that proxy is the sole way to reach this port and always overwrites it, for every request; otherwise anyone who can reach this port directly could set it themselves and sign in as anyone. Empty (default) disables the endpoint entirely; env CONTINUUM_ADMIN_SSO_HEADER")
	agentBehindProxy := flag.Bool("agent-behind-proxy", os.Getenv("CONTINUUM_AGENT_BEHIND_PROXY") == "true", "an L4 load balancer or reverse proxy sits in front of --agent-listen and is configured to send a PROXY protocol header (v1 or v2) ahead of each connection - the way to preserve the real client address through a TCP passthrough, since the agent's own mTLS handshake rules out a TLS-terminating HTTP proxy here. With it, every connection must carry that header (one that doesn't is refused) and its declared address - not the proxy's own - is what approval cards, rate limits, the audit trail and an agent's suggested location use. Only set it when the proxy is the sole way to reach this port and is actually configured to send the header; env CONTINUUM_AGENT_BEHIND_PROXY=true")
	deciderAllow := flag.String("decider-allow-cidrs", os.Getenv("CONTINUUM_DECIDER_ALLOW_CIDRS"), "comma-separated CIDRs (10.0.0.0/8,127.0.0.1/32) the server may call for the external decider although they are private or loopback. By default only public addresses are allowed, so a decider address cannot be used to reach internal services. Cloud metadata and link-local addresses are never allowed; env CONTINUUM_DECIDER_ALLOW_CIDRS")
	caPassFile := flag.String("ca-key-passphrase-file", os.Getenv("CONTINUUM_CA_KEY_PASSPHRASE_FILE"), "file holding a passphrase (at least 12 characters) that encrypts the CA private key at rest (argon2id + AES-256-GCM). A plaintext key is encrypted the first time this is given; an encrypted key without it stops the server. Never give the passphrase itself as a flag value; env CONTINUUM_CA_KEY_PASSPHRASE_FILE. Without it the key is stored unencrypted (mode 0600) and a warning is logged. See internal/pki/ROTATION.md")
	looseOK := flag.Bool("allow-loose-permissions", os.Getenv("CONTINUUM_ALLOW_LOOSE_PERMISSIONS") == "true", "start although the data directory, the CA key or the database is readable or writable by group or other users (by default the server refuses and prints the chmod to run). For platforms that set modes themselves, such as a Kubernetes fsGroup volume only this pod can reach; env CONTINUUM_ALLOW_LOOSE_PERMISSIONS=true")
	uiDir := flag.String("ui-dir", "", "built UI to serve (dist/)")
	chartRef := flag.String("chart-ref", "", "Helm chart to install, as shown in install commands (an OCI, repository or https reference). Empty: oci://<image-registry>/continuum-agent. \"local\": the copy of the chart this server serves, which the wizard offers as a download")
	imageRegistry := flag.String("image-registry", os.Getenv("CONTINUUM_IMAGE_REGISTRY"), "registry and path that hold the agent image and chart you published (registry.example.com/team, or a bare Docker Hub name; see scripts/publish.sh). Install commands then point the chart at <registry>/continuum and fetch the chart itself from the same place as an OCI artifact. There is no built-in default: with none, the command uses the chart's own image name (continuum/continuum) and the chart file this server serves. This is the server-wide default; an organisation's Settings → Installation overrides it; env CONTINUUM_IMAGE_REGISTRY")
	imageTag := flag.String("image-tag", os.Getenv("CONTINUUM_IMAGE_TAG"), "image tag for install commands; empty means the chart's own appVersion. Needs --image-registry; env CONTINUUM_IMAGE_TAG")
	imageDigest := flag.String("image-digest", os.Getenv("CONTINUUM_IMAGE_DIGEST"), "image digest (sha256:<64 hex>, printed by scripts/publish.sh) to pin install commands to, which makes them immutable and reproducible; the tag is then ignored by the chart. Needs --image-registry; env CONTINUUM_IMAGE_DIGEST")
	org := flag.String("org", "default", "id of the organization created together with the first account, on a fresh database")
	registration := flag.String("registration", os.Getenv("CONTINUUM_REGISTRATION"), "who may create an account: open (anyone, and they get an organization of their own), invite (only with an invitation from an organization) or closed (nobody; use the create-org and reset-password commands); env CONTINUUM_REGISTRATION. Default: open while --admin-listen is a loopback address (local use), invite otherwise")
	geoDB := flag.String("geoip-db", os.Getenv("CONTINUUM_GEOIP_DB"), "optional MaxMind-format .mmdb (DB-IP Lite, GeoLite2) used offline to suggest a location for each agent's connecting address; env CONTINUUM_GEOIP_DB")
	geoPublicIP := flag.String("geoip-public-ip-service", os.Getenv("CONTINUUM_GEOIP_PUBLIC_IP_SERVICE"), "needs --geoip-db. An http(s) URL that answers with this server's own public IP as plain text (a well known one: https://api.ipify.org). Used only as a fallback: when an agent's connecting address cannot be located at all (private, loopback, CGNAT - agent and server sharing a network is exactly why it looks that way), this server's own public address is the closest honest guess, looked up and returned marked estimated rather than exact. Called once at startup and then on an hourly cache, never per request; empty (default) never guesses, only an agent's own address is ever used. Nothing about your topology, applications or data goes out in the request - just an empty GET, to a URL you chose; env CONTINUUM_GEOIP_PUBLIC_IP_SERVICE")
	geoASNDB := flag.String("geoip-asn-db", os.Getenv("CONTINUUM_GEOIP_ASN_DB"), "needs --geoip-db. A second, optional MaxMind-format .mmdb in ASN shape (GeoLite2-ASN, DB-IP ASN Lite) rather than city/country shape. Whenever --geoip-db places an address (including an --geoip-public-ip-service estimate), this adds which network it belongs to - its AS number and the organisation that announces it, e.g. 15169 / \"Google LLC\" - a steadier signal than city or country, and unaffected by a VPN or cloud egress moving the place shown. Shown only alongside a location, never on its own: an address --geoip-db has no record for stays unanswered even when this database recognises it. Entirely offline, same as --geoip-db: nothing is looked up over the network; env CONTINUUM_GEOIP_ASN_DB")
	neoURL := flag.String("neo4j-url", os.Getenv("CONTINUUM_NEO4J_URL"), "Neo4j HTTP address (http://host:7474). When set, topology history, events, audit and workspace revisions are kept in a Neo4j graph; env CONTINUUM_NEO4J_URL")
	neoUser := flag.String("neo4j-user", envOr("CONTINUUM_NEO4J_USER", "neo4j"), "Neo4j user; env CONTINUUM_NEO4J_USER")
	neoDB := flag.String("neo4j-database", envOr("CONTINUUM_NEO4J_DATABASE", "neo4j"), "Neo4j database name; env CONTINUUM_NEO4J_DATABASE")
	neoPassFile := flag.String("neo4j-password-file", os.Getenv("CONTINUUM_NEO4J_PASSWORD_FILE"), "file holding the Neo4j password (or set CONTINUUM_NEO4J_PASSWORD); env CONTINUUM_NEO4J_PASSWORD_FILE")
	neoInsecure := flag.Bool("neo4j-allow-insecure-http", os.Getenv("CONTINUUM_NEO4J_ALLOW_INSECURE_HTTP") == "true", "allow the Neo4j password and the topology to be sent over plain HTTP to a host that is not loopback. By default the server refuses to start: use an https:// address, or, on a network you trust (a pod-to-pod link inside one cluster, say), pass this flag; env CONTINUUM_NEO4J_ALLOW_INSECURE_HTTP=true")
	smtpHost := flag.String("smtp-host", os.Getenv("CONTINUUM_SMTP_HOST"), "SMTP server for outgoing mail (a login or email-verification code - nothing else is ever sent), and the default until an owner of the default organisation sets one in Settings instead. Without either, email is simply not offered as a second factor; env CONTINUUM_SMTP_HOST")
	smtpPort := flag.String("smtp-port", envOr("CONTINUUM_SMTP_PORT", "587"), "SMTP submission port. STARTTLS is used automatically when the server offers it; env CONTINUUM_SMTP_PORT")
	smtpUser := flag.String("smtp-username", os.Getenv("CONTINUUM_SMTP_USERNAME"), "SMTP username, if the relay requires authentication; env CONTINUUM_SMTP_USERNAME")
	smtpPassFile := flag.String("smtp-password-file", os.Getenv("CONTINUUM_SMTP_PASSWORD_FILE"), "file holding the SMTP password (or set CONTINUUM_SMTP_PASSWORD); never given as a flag value, which would show in the process list; env CONTINUUM_SMTP_PASSWORD_FILE")
	smtpFrom := flag.String("smtp-from", os.Getenv("CONTINUUM_SMTP_FROM"), "From: address on mailed codes. Required with --smtp-host; env CONTINUUM_SMTP_FROM")
	flag.Var(&origins, "allow-origin", "browser origin allowed to call the API cross-origin (repeatable, development)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *agentAddr == "" {
		fatal(log, errors.New("--agent-address is required, for example continuum.example.com:8443"))
	}
	regMode, regWhy := resolveRegistration(*registration, *adminListen)
	switch regMode {
	case server.RegOpen, server.RegInvite, server.RegClosed:
	default:
		fatal(log, fmt.Errorf("--registration must be open, invite or closed, not %q", regMode))
	}
	log.Info("account registration", "mode", regMode, "why", regWhy)
	if regMode == server.RegOpen && !isLoopback(*adminListen) {
		log.Warn("registration is open on a non-loopback address: anyone who can reach the admin address can create an account and an organization. Fine for a private network; on an exposed server use --registration invite", "admin", *adminListen)
	}
	decider, err := server.NewDeciderPolicy(*deciderAllow)
	if err != nil {
		fatal(log, err)
	}
	if a := decider.Allowed(); len(a) > 0 {
		log.Warn("the external decider may be a private address in these ranges (--decider-allow-cidrs); cloud metadata and link-local addresses stay blocked", "ranges", a)
	}
	var neo *graph.Config
	if *neoURL != "" {
		pw := os.Getenv("CONTINUUM_NEO4J_PASSWORD")
		if *neoPassFile != "" {
			b, err := os.ReadFile(*neoPassFile)
			if err != nil {
				fatal(log, fmt.Errorf("--neo4j-password-file: %w", err))
			}
			pw = strings.TrimSpace(string(b))
		}
		if pw == "" {
			fatal(log, errors.New("--neo4j-url needs a password: set CONTINUUM_NEO4J_PASSWORD or --neo4j-password-file (it is not accepted as a flag value, which would show in the process list)"))
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
	img, err := server.ImageConfig{Registry: *imageRegistry, Tag: *imageTag, Digest: *imageDigest}.Normalize()
	if err != nil {
		fatal(log, fmt.Errorf("--image-registry/--image-tag/--image-digest: %w", err))
	}
	var mail server.MailConfig
	if *smtpHost != "" {
		if *smtpFrom == "" {
			fatal(log, errors.New("--smtp-host needs --smtp-from"))
		}
		pw := os.Getenv("CONTINUUM_SMTP_PASSWORD")
		if *smtpPassFile != "" {
			b, err := os.ReadFile(*smtpPassFile)
			if err != nil {
				fatal(log, fmt.Errorf("--smtp-password-file: %w", err))
			}
			pw = strings.TrimSpace(string(b))
		}
		mail = server.MailConfig{Host: *smtpHost, Port: *smtpPort, Username: *smtpUser, Password: pw, From: *smtpFrom}
	}
	if err := run(log, *dataDir, *agentListen, *agentAddr, *agentExposure, *releaseName, *releaseNamespace, *extraHosts, *adminListen, *adminCert, *adminKey, *behindProxy, *agentBehindProxy, *ssoHeader, *uiDir, *chartRef, img, *org, regMode, *geoDB, *geoPublicIP, *geoASNDB, decider, neo, mail, keyOpts{PassphraseFile: *caPassFile, AllowLoose: *looseOK}, origins); err != nil {
		fatal(log, err)
	}
}

// keyOpts is how the operator asked for the CA key and the data directory to be protected.
type keyOpts struct {
	PassphraseFile string
	AllowLoose     bool
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

func run(log *slog.Logger, dataDir, agentListen, agentAddr, agentExposure, releaseName, releaseNamespace, extraHosts, adminListen, adminCert, adminKey string, behindProxy, agentBehindProxy bool, ssoHeader, uiDir, chartRef string, img server.ImageConfig, org, registration, geoPath, geoPublicIP, geoASNPath string, decider *server.DeciderPolicy, neo *graph.Config, mail server.MailConfig, keys keyOpts, origins []string) error {
	if err := checkSSOHeader(ssoHeader, behindProxy); err != nil {
		return err
	}
	// Fail closed: a database that was asked for but cannot be used stops the server rather than silently turning the feature off.
	var geo *server.Geo
	if geoPath != "" {
		db, err := geoip.Open(geoPath)
		if err != nil {
			return fmt.Errorf("--geoip-db %s: %w", geoPath, err)
		}
		geo = server.NewGeo(db)
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
	var passphrase []byte
	if keys.PassphraseFile != "" {
		var err error
		if passphrase, err = readPassphraseFile(log, keys.PassphraseFile); err != nil {
			return err
		}
	}
	ca, err := pki.LoadOrCreateWith(filepath.Join(dataDir, "pki"), pki.Options{Passphrase: passphrase, Log: log})
	if err != nil {
		return err
	}
	sqlite, err := store.OpenSQLite(filepath.Join(dataDir, "continuum.db"))
	if err != nil {
		return err
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
		finder := server.NewPublicIPFinder(geoPublicIP, 0)
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
	// mail is the --smtp-* flags' value: the boot-time default, in effect until an owner of the default
	// organisation saves one through Settings, and again after that if the database is ever reset without
	// the flags changing to match (see SetMailerDefault, LoadMailConfig).
	core.SetMailerDefault(mail)
	core.LoadMailConfig(context.Background())
	core.PendingTTL, core.RefuseLegacyApproval = *pendingTTL, *refuseLegacy
	core.TrustAgentProxy = agentBehindProxy
	// Unlike Mailer, passkeys need no operator configuration - go-webauthn works out of the relying party
	// info Admin derives from each request's own Host header, so the provider is simply always on.
	core.WebAuthn = passkey.New()
	created, firstPassword, err := core.BootstrapAdmin(context.Background(), os.Getenv("CONTINUUM_ADMIN_PASSWORD"))
	if err != nil {
		return err
	}
	platform := server.NewPlatform(core, geo)
	if err := platform.Start(runCtx); err != nil {
		return err
	}
	grpcSrv := core.NewGRPC(certs, platform)

	l, err := net.Listen("tcp", agentListen)
	if err != nil {
		return err
	}
	go func() {
		if err := grpcSrv.Serve(l); err != nil {
			log.Error("agent listener stopped", "err", err)
		}
	}()

	admin := &server.Admin{P: platform, C: core, TrustProxy: behindProxy, SSOHeaderName: ssoHeader, SecureCookies: !isLoopback(adminListen), AgentAddr: agentAddr, AgentExposure: agentExposure, ReleaseName: releaseName, ReleaseNamespace: releaseNamespace, ChartRef: chartRef, ImageRegistry: img.Registry, ImageTag: img.Tag, ImageDigest: img.Digest, Origins: origins, UIDir: uiDir, Version: version, AgentChartVersion: agentChartVersion}
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
		}
	}()

	log.Info("continuum server started", "agents", agentListen, "admin", adminListen, "registration", registration, "ca_pin", ca.Pin(), "ca_spki_pin", ca.SPKIPin())
	if created && firstPassword != "" {
		// Shown once, never stored in readable form. It must be replaced at first sign-in.
		fmt.Fprintf(os.Stderr, "\n  First start: sign in as  admin  with the one-time password  %s\n  You will be asked to choose a new password straight away.\n\n", firstPassword)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "admin-token")); err == nil {
		log.Warn("the shared admin token is no longer used; sign in with a user account. You can delete this file", "file", filepath.Join(dataDir, "admin-token"))
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(ctx)
	}
	// Agents' streams never end on their own, so a graceful stop would wait for them forever: give them a few seconds, then close.
	grpcSrv.StopWithin(5 * time.Second)
	return nil
}

// resolveRegistration picks the registration mode. An explicit flag or environment value always wins. Without
// one, a server that listens only on loopback (local use) keeps registration open so that it just works, and
// any other listener defaults to invitation-only: an exposed server must not let strangers create accounts
// unless the operator says so. why is what the startup log tells the operator.
func resolveRegistration(explicit, adminListen string) (mode, why string) {
	if explicit != "" {
		return explicit, "set explicitly by --registration or CONTINUUM_REGISTRATION"
	}
	if isLoopback(adminListen) {
		return server.RegOpen, "default: the admin listener " + adminListen + " is loopback, so only this machine can reach it"
	}
	return server.RegInvite, "default: the admin listener " + adminListen + " is not loopback, so accounts are by invitation only (pass --registration open to allow anyone to sign up)"
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
	path := filepath.Join(*dataDir, "continuum.db")
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
		return fmt.Errorf("refusing to send the Neo4j password over plain HTTP to %s: it and the topology would cross the network in clear text. Use an https:// address, or pass --neo4j-allow-insecure-http (env CONTINUUM_NEO4J_ALLOW_INSECURE_HTTP=true) if the network between the two is one you trust", u.Host)
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
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: server reset-password [--data-dir DIR] USERNAME") }
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.OpenSQLite(filepath.Join(*dataDir, "continuum.db"))
	if err != nil {
		fatal(log, err)
	}
	defer st.Close()
	pw, err := server.NewCore(st, nil, "", log).RecoverPassword(context.Background(), fs.Arg(0))
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
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: server create-org [--data-dir DIR] --owner USERNAME NAME")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 || *owner == "" {
		fs.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.OpenSQLite(filepath.Join(*dataDir, "continuum.db"))
	if err != nil {
		fatal(log, err)
	}
	defer st.Close()
	o, err := server.NewCore(st, nil, "", log).CreateOrgFor(context.Background(), *owner, fs.Arg(0))
	if err != nil {
		fatal(log, err)
	}
	fmt.Printf("Created organization %q (%s), owned by %s.\n", o.Name, o.ID, *owner)
}
