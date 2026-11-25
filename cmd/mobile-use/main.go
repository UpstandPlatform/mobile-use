package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mobile-use/internal/agent"
	"mobile-use/internal/client"
	"mobile-use/internal/connector"
	"mobile-use/internal/relay"
	"mobile-use/internal/service"
	"mobile-use/internal/store"
	"mobile-use/internal/webui"
	"mobile-use/internal/wire"
)

var version = "development"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println(`mobile-use

  mobile-use init                       Create local owner and agent credentials
  mobile-use serve                      Run the local daemon and console
  mobile-use serve --help               Show server and self-hosting options
  mobile-use connector --help           Run a remote device connector
  mobile-use mcp                        Serve MCP over stdio
  mobile-use token admin|agent          Print a local credential
  mobile-use devices                    List devices as the selected API identity
  mobile-use call METHOD /v1/path [FILE]  Call the API with optional JSON from FILE or -
  mobile-use doctor                     Check local Android availability
  mobile-use pki --help                 Create a private CA or issue a certificate
  mobile-use version                     Print build version`)
		return nil
	}
	switch args[0] {
	case "init":
		return initialize()
	case "version":
		fmt.Println(version)
		return nil
	case "serve":
		return serve(ctx, args[1:])
	case "connector":
		return connect(ctx, args[1:])
	case "mcp":
		api, err := newClient(false)
		if err != nil {
			return err
		}
		return agent.New(agent.Remote{Client: api}).Run(ctx, &mcp.StdioTransport{})
	case "call", "devices":
		return call(ctx, args)
	case "token":
		if len(args) != 2 || args[1] != "admin" && args[1] != "agent" {
			return fmt.Errorf("usage: mobile-use token admin|agent")
		}
		cfg, err := readConfig()
		if err != nil {
			return err
		}
		if args[1] == "admin" {
			fmt.Println(cfg.AdminToken)
		} else {
			fmt.Println(cfg.AgentToken)
		}
		return nil
	case "doctor":
		probe, stop := context.WithTimeout(ctx, 6*time.Second)
		defer stop()
		devices, err := (connector.Android{ADB: os.Getenv("MOBILE_USE_ADB")}).Discover(probe)
		result := map[string]any{"adb_available": err == nil, "authorized_android_devices": len(devices), "version": version}
		if err != nil {
			result["adb_error"] = err.Error()
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "pki":
		return pki(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run mobile-use help", args[0])
	}
}

type config struct {
	AdminToken string `json:"admin_token"`
	AgentToken string `json:"agent_token"`
	DigestKey  string `json:"digest_key"`
}

func configDir() (string, error) {
	if value := os.Getenv("MOBILE_USE_CONFIG_DIR"); value != "" {
		return filepath.Abs(value)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mobile-use"), nil
}

func initialize() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	value := config{AdminToken: wire.NewID() + wire.NewID(), AgentToken: wire.NewID() + wire.NewID(), DigestKey: wire.NewID() + wire.NewID()}
	if err := writeNew(path, []byte(wire.Marshal(value)), 0600); err != nil {
		return err
	}
	fmt.Println("Created credentials in", path)
	return nil
}

func readConfig() (config, error) {
	var value config
	dir, err := configDir()
	if err != nil {
		return value, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return value, fmt.Errorf("run mobile-use init or configure a remote server and token")
	}
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal(data, &value); err != nil {
		return value, err
	}
	if len(value.AdminToken) < 32 || len(value.AgentToken) < 32 || value.AdminToken == value.AgentToken || len(value.DigestKey) < 64 {
		return value, fmt.Errorf("invalid local configuration")
	}
	return value, nil
}

func writeNew(path string, data []byte, perms os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perms)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type backendFlags struct {
	adb            *string
	noAndroid      *bool
	androidHelper  *bool
	scrcpyServer   *string
	iosFFmpeg      *string
	androidCatalog *string
	aapt2          *string
	appium         *string
	ios            *string
}

func platformFlags(f *flag.FlagSet) backendFlags {
	return backendFlags{
		adb:            f.String("adb", env("MOBILE_USE_ADB", "adb"), "Path to the Android platform-tools adb executable"),
		noAndroid:      f.Bool("no-android", false, "Disable local Android discovery"),
		androidHelper:  f.Bool("android-control-helper", env("MOBILE_USE_ANDROID_CONTROL_HELPER", "") == "1", "Use the trusted mobile-use instrumentation APK for Unicode and multi-touch"),
		scrcpyServer:   f.String("scrcpy-server", os.Getenv("MOBILE_USE_SCRCPY_SERVER"), "Verified scrcpy 4.0 server file for Android live video"),
		iosFFmpeg:      f.String("ios-ffmpeg", env("MOBILE_USE_IOS_FFMPEG", "ffmpeg"), "FFmpeg executable for the iOS MJPEG video tier"),
		androidCatalog: f.String("android-catalog", os.Getenv("MOBILE_USE_ANDROID_CATALOG"), "Connector-managed Android deployment and reset catalog"),
		aapt2:          f.String("aapt2", env("MOBILE_USE_AAPT2", "aapt2"), "Android Build Tools aapt2 executable for catalog APK checks"),
		appium:         f.String("appium", env("MOBILE_USE_APPIUM", ""), "Loopback URL of an isolated Appium instance"),
		ios:            f.String("ios-config", env("MOBILE_USE_IOS_CONFIG", ""), "JSON file with the allowed iOS devices and WDA settings"),
	}
}

func (b backendFlags) backends() ([]connector.Backend, error) {
	var backends []connector.Backend
	if !*b.noAndroid {
		if *b.scrcpyServer != "" {
			if err := connector.VerifyScrcpy(*b.scrcpyServer); err != nil {
				return nil, err
			}
		}
		var catalog *connector.AndroidCatalog
		if *b.androidCatalog != "" {
			var err error
			catalog, err = connector.LoadAndroidCatalog(*b.androidCatalog, *b.aapt2)
			if err != nil {
				return nil, err
			}
		}
		backends = append(backends, connector.Android{ADB: *b.adb, UseHelper: *b.androidHelper, ScrcpyServer: *b.scrcpyServer, Catalog: catalog})
	}
	if *b.appium != "" || *b.ios != "" {
		data, err := os.ReadFile(*b.ios)
		if err != nil {
			return nil, err
		}
		var devices []connector.IOSDevice
		if err := json.Unmarshal(data, &devices); err != nil {
			return nil, err
		}
		ios, err := connector.NewIOS(*b.appium, devices, *b.iosFFmpeg)
		if err != nil {
			return nil, err
		}
		backends = append(backends, ios)
	}
	return backends, nil
}

func serve(parent context.Context, args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := f.String("listen", env("MOBILE_USE_LISTEN", "127.0.0.1:8787"), "Console and agent API listen address")
	origin := f.String("origin", env("MOBILE_USE_ORIGIN", "http://127.0.0.1:8787"), "Exact public HTTP origin including its port")
	postgres := f.String("postgres", os.Getenv("MOBILE_USE_POSTGRES"), "PostgreSQL connection string; enables self-hosted mode")
	issuer := f.String("oidc-issuer", os.Getenv("MOBILE_USE_OIDC_ISSUER"), "Trusted HTTPS OAuth issuer")
	audience := f.String("oidc-audience", os.Getenv("MOBILE_USE_OIDC_AUDIENCE"), "Required API access token audience")
	consoleClient := f.String("oidc-console-client", os.Getenv("MOBILE_USE_CONSOLE_CLIENT"), "Pre-registered public OAuth client for the console")
	httpCert := f.String("http-cert", os.Getenv("MOBILE_USE_HTTP_CERT"), "HTTPS certificate")
	httpKey := f.String("http-key", os.Getenv("MOBILE_USE_HTTP_KEY"), "HTTPS private key")
	relayListen := f.String("relay-listen", os.Getenv("MOBILE_USE_RELAY_LISTEN"), "mTLS connector listener")
	relayAddress := f.String("relay-address", os.Getenv("MOBILE_USE_RELAY_ADDRESS"), "Per-replica peer address in host:port form")
	relayCert := f.String("relay-cert", os.Getenv("MOBILE_USE_RELAY_CERT"), "Connector relay TLS certificate")
	relayKey := f.String("relay-key", os.Getenv("MOBILE_USE_RELAY_KEY"), "Connector relay TLS private key")
	relayCA := f.String("relay-ca", os.Getenv("MOBILE_USE_RELAY_CA"), "Private CA for connectors and relay peers")
	companionListen := f.String("companion-listen", os.Getenv("MOBILE_USE_COMPANION_LISTEN"), "Optional LAN-only companion TLS listener in workstation mode")
	companionOrigin := f.String("companion-origin", os.Getenv("MOBILE_USE_COMPANION_ORIGIN"), "HTTPS origin reached by Android companion")
	companionCert := f.String("companion-cert", os.Getenv("MOBILE_USE_COMPANION_CERT"), "Companion HTTPS certificate")
	companionKey := f.String("companion-key", os.Getenv("MOBILE_USE_COMPANION_KEY"), "Companion HTTPS key")
	connectorMap := f.String("connectors", os.Getenv("MOBILE_USE_CONNECTORS"), "Approved connector common names as name=workspace pairs")
	peerNames := f.String("relay-peers", os.Getenv("MOBILE_USE_RELAY_PEERS"), "Comma-separated common names of trusted replica clients")
	platforms := platformFlags(f)
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected daemon arguments")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("listen address must be host:port")
	}
	u, err := url.Parse(*origin)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("origin must be a bare HTTP or HTTPS origin")
	}
	if u.Scheme == "https" && (*httpCert == "" || *httpKey == "") {
		return fmt.Errorf("HTTPS origin requires --http-cert and --http-key")
	}
	if u.Scheme == "http" && !loopback(host) {
		return fmt.Errorf("an HTTP listener must be loopback-only")
	}
	var cfg config
	var sqlitePath string
	var principals []service.Principal
	if *postgres == "" {
		if !loopback(host) || !loopback(u.Hostname()) {
			return fmt.Errorf("workstation mode is loopback-only")
		}
		cfg, err = readConfig()
		if err != nil {
			return err
		}
		dir, err := configDir()
		if err != nil {
			return err
		}
		sqlitePath = filepath.Join(dir, "mobile-use.sqlite")
		principals = []service.Principal{{Token: cfg.AdminToken, Actor: wire.Actor{Workspace: "local", Subject: "local-owner", Role: wire.Admin}}, {Token: cfg.AgentToken, Actor: wire.Actor{Workspace: "local", Subject: "local-agent", Role: wire.Agent}}}
	} else {
		if *issuer == "" || u.Scheme != "https" {
			return fmt.Errorf("self-hosting requires HTTPS and a trusted OIDC issuer")
		}
		cfg.DigestKey = os.Getenv("MOBILE_USE_DIGEST_KEY")
	}
	key, err := hex.DecodeString(cfg.DigestKey)
	if err != nil || len(key) < 32 {
		return fmt.Errorf("configure a shared 32-byte hexadecimal operation digest key")
	}
	startup, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	auth, err := service.NewAuth(startup, *issuer, *audience, *consoleClient, principals)
	if err != nil {
		return err
	}
	db, err := store.Open(startup, sqlitePath, *postgres)
	if err != nil {
		return err
	}
	defer db.Close()
	hub := &relay.Hub{Store: db, RelayAddress: *relayAddress, PeerNames: make(map[string]bool)}
	if *postgres == "" {
		backends, err := platforms.backends()
		if err != nil {
			return err
		}
		hub.LocalWorkspace = "local"
		hub.Local = connector.New("local", "local", backends...)
		defer hub.Local.Disconnect()
		go localInventory(ctx, db, hub.Local)
	}
	failures := make(chan error, 3)
	if *relayListen != "" {
		hub.ConnectorNames, err = parseConnectorMap(*connectorMap)
		if err != nil {
			return err
		}
		for _, name := range strings.Split(*peerNames, ",") {
			if name == "" {
				continue
			}
			if !wire.ValidID(name) || hub.ConnectorNames[name] != "" {
				return fmt.Errorf("invalid or conflicting relay peer identity")
			}
			hub.PeerNames[name] = true
		}
		tlsConfig, err := relay.TLS(*relayCert, *relayKey, *relayCA, "", true)
		if err != nil {
			return err
		}
		if *relayAddress != "" {
			if _, _, err := net.SplitHostPort(*relayAddress); err != nil {
				return fmt.Errorf("relay address must be host:port")
			}
			hub.PeerTLS, err = relay.TLS(*relayCert, *relayKey, *relayCA, "", false)
			if err != nil {
				return err
			}
		}
		go func() { failures <- hub.Serve(ctx, *relayListen, tlsConfig) }()
	}
	svc := &service.Service{Store: db, Hub: hub, DigestKey: key}
	video, err := service.NewVideo(ctx, db, hub, os.Getenv("MOBILE_USE_TURN_URLS"), []byte(os.Getenv("MOBILE_USE_TURN_SECRET")), os.Getenv("MOBILE_USE_TURN_RELAY_ONLY") == "1")
	if err != nil {
		return err
	}
	svc.Video = video
	go svc.Maintain(ctx)
	var companionServer *http.Server
	var extra func(*http.ServeMux)
	if os.Getenv("MOBILE_USE_COMPANION_DISABLED") != "1" && (*companionOrigin != "" || *postgres != "") {
		phoneOrigin := *companionOrigin
		if phoneOrigin == "" {
			phoneOrigin = *origin
		}
		phoneURL, err := url.Parse(phoneOrigin)
		if err != nil || phoneURL.Scheme != "https" || phoneURL.Host == "" || phoneURL.Path != "" || phoneURL.User != nil || phoneURL.Fragment != "" || phoneURL.RawQuery != "" {
			return fmt.Errorf("companion origin must be a bare HTTPS origin")
		}
		companion := service.NewCompanion(db, phoneOrigin)
		hub.Companion = companion
		go companion.Maintain(ctx)
		extra = func(router *http.ServeMux) {
			companion.ConsoleRoutes(router, auth, *origin)
			if phoneOrigin == *origin && *companionListen == "" {
				companion.PhoneRoutes(router)
			}
		}
		if phoneOrigin != *origin || *companionListen != "" {
			if *companionListen == "" || *companionCert == "" || *companionKey == "" {
				return fmt.Errorf("a separate companion origin requires a TLS listener, certificate, and key")
			}
			companionServer = &http.Server{Addr: *companionListen, Handler: companion.PhoneHandler(), ReadHeaderTimeout: 4 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 25 * time.Second, MaxHeaderBytes: 8 << 10}
			go func() { failures <- companionServer.ListenAndServeTLS(*companionCert, *companionKey) }()
		}
	}
	handler := svc.HTTP(service.HTTPOptions{Origin: *origin, Auth: auth, Console: webui.Handler(), MCP: agent.HTTP(svc), Extra: extra, Video: video})
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 45 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		if u.Scheme == "https" {
			failures <- server.ListenAndServeTLS(*httpCert, *httpKey)
		} else {
			failures <- server.ListenAndServe()
		}
	}()
	slog.Info("mobile-use listening", "origin", *origin, "version", version)
	var failure error
	select {
	case <-ctx.Done():
	case failure = <-failures:
	}
	cancel()
	shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	if companionServer != nil {
		_ = companionServer.Shutdown(shutdown)
	}
	if errors.Is(failure, http.ErrServerClosed) {
		return nil
	}
	return failure
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseConnectorMap(raw string) (map[string]string, error) {
	result := make(map[string]string)
	for _, value := range strings.Split(raw, ",") {
		if value == "" {
			continue
		}
		name, space, ok := strings.Cut(value, "=")
		if !ok || !wire.ValidID(name) || !wire.ValidID(space) || name == "local" || result[name] != "" {
			return nil, fmt.Errorf("invalid connector allowlist")
		}
		result[name] = space
	}
	return result, nil
}

func localInventory(ctx context.Context, db *store.Store, manager *connector.Manager) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		discover, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		inventory := manager.Inventory(discover)
		cancel()
		if err := db.Register(ctx, "local", "local", inventory, time.Now()); err != nil && ctx.Err() == nil {
			slog.Warn("Could not refresh local devices", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func connect(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("connector", flag.ContinueOnError)
	address := f.String("endpoint", os.Getenv("MOBILE_USE_CONNECTOR_ENDPOINT"), "Remote mTLS relay address in host:port form")
	name := f.String("name", os.Getenv("MOBILE_USE_CONNECTOR_NAME"), "Enrolled connector identity (certificate common name)")
	space := f.String("workspace", os.Getenv("MOBILE_USE_CONNECTOR_WORKSPACE"), "Workspace used to derive stable device IDs")
	cert := f.String("cert", os.Getenv("MOBILE_USE_CONNECTOR_CERT"), "Client certificate")
	key := f.String("key", os.Getenv("MOBILE_USE_CONNECTOR_KEY"), "Client private key")
	ca := f.String("ca", os.Getenv("MOBILE_USE_CONNECTOR_CA"), "Server CA certificate")
	platforms := platformFlags(f)
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || !wire.ValidID(*name) || !wire.ValidID(*space) || *name == "local" {
		return fmt.Errorf("set a valid connector name and workspace")
	}
	config, err := relay.TLS(*cert, *key, *ca, "", false)
	if err != nil {
		return err
	}
	if config.Certificates[0].Leaf.Subject.CommonName != *name {
		return fmt.Errorf("connector certificate common name must match --name")
	}
	backends, err := platforms.backends()
	if err != nil {
		return err
	}
	return relay.Connect(ctx, *address, config, connector.New(*space, *name, backends...))
}

func newClient(admin bool) (*client.Client, error) {
	token := os.Getenv("MOBILE_USE_TOKEN")
	if token == "" {
		cfg, err := readConfig()
		if err != nil {
			return nil, err
		}
		token = cfg.AgentToken
		if admin {
			token = cfg.AdminToken
		}
	}
	return client.New(env("MOBILE_USE_URL", "http://127.0.0.1:8787"), token)
}

func call(ctx context.Context, args []string) error {
	api, err := newClient(false)
	if err != nil {
		return err
	}
	method, path := "GET", "/v1/devices"
	var body json.RawMessage
	if args[0] == "call" {
		if len(args) < 3 || len(args) > 4 {
			return fmt.Errorf("usage: mobile-use call METHOD /v1/path [FILE|-]")
		}
		method, path = args[1], args[2]
		if len(args) == 4 {
			var reader io.Reader = os.Stdin
			if args[3] != "-" {
				file, err := os.Open(args[3])
				if err != nil {
					return err
				}
				defer file.Close()
				reader = file
			}
			body, err = io.ReadAll(io.LimitReader(reader, (128<<10)+1))
			if err != nil {
				return err
			}
			if len(body) > 128<<10 || !json.Valid(body) {
				return fmt.Errorf("invalid or oversized JSON request")
			}
		}
	}
	var input any
	if body != nil {
		input = body
	}
	var output json.RawMessage
	if err := api.Request(ctx, method, path, input, &output); err != nil {
		return err
	}
	fmt.Print(string(output))
	return nil
}
