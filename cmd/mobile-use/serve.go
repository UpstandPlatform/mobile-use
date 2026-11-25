package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mobile-use/internal/agent"
	"mobile-use/internal/connector"
	"mobile-use/internal/lan"
	"mobile-use/internal/relay"
	"mobile-use/internal/service"
	"mobile-use/internal/store"
	"mobile-use/internal/webui"
	"mobile-use/internal/wire"
)

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

type ticketStore struct {
	mu      sync.Mutex
	tickets map[string]time.Time
}

func (t *ticketStore) create() string {
	now := time.Now()
	id := wire.NewID()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tickets == nil {
		t.tickets = make(map[string]time.Time)
	}
	for k, exp := range t.tickets {
		if !now.Before(exp) {
			delete(t.tickets, k)
		}
	}
	if len(t.tickets) >= 16 {
		clear(t.tickets)
	}
	t.tickets[id] = now.Add(30 * time.Second)
	return id
}

func (t *ticketStore) exchange(id string) bool {
	if id == "" {
		return false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	exp, ok := t.tickets[id]
	if ok {
		delete(t.tickets, id)
	}
	return ok && now.Before(exp)
}

func serve(parent context.Context, args []string, upMode bool) error {
	name := "serve"
	if upMode {
		name = "up"
	}
	f := flag.NewFlagSet(name, flag.ContinueOnError)
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
	noCompanion := f.Bool("no-companion", false, "Disable automatic Wi-Fi companion listener in workstation mode")
	openUI := f.Bool("open", upMode, "Open the desktop web console in the default browser on startup")
	noOpen := f.Bool("no-open", false, "Do not open the desktop web console automatically")
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
	shouldOpen := *openUI && !*noOpen

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	host, listenPort, err := net.SplitHostPort(*listen)
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
	var dir string
	var sqlitePath string
	var principals []service.Principal
	workstation := *postgres == ""
	if workstation {
		if !loopback(host) || !loopback(u.Hostname()) {
			return fmt.Errorf("workstation mode is loopback-only")
		}
		if upMode {
			var created bool
			var cfgPath string
			cfg, cfgPath, created, err = ensureConfig()
			if err != nil {
				return err
			}
			if created {
				fmt.Println("Initialized credentials in", cfgPath)
			}
		} else {
			cfg, err = readConfig()
			if err != nil {
				return err
			}
		}
		dir, err = configDir()
		if err != nil {
			return err
		}
		sqlitePath = filepath.Join(dir, "mobile-use.sqlite")
		principals = []service.Principal{
			{Token: cfg.AdminToken, Actor: wire.Actor{Workspace: "local", Subject: "local-owner", Role: wire.Admin}},
			{Token: cfg.AgentToken, Actor: wire.Actor{Workspace: "local", Subject: "local-agent", Role: wire.Agent}},
		}
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
	if workstation {
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
	var companion *service.Companion
	var mdnsActive bool
	companionEnabledEnv := os.Getenv("MOBILE_USE_COMPANION_DISABLED") != "1" && !*noCompanion

	if companionEnabledEnv {
		if *companionOrigin != "" || !workstation {
			phoneOrigin := *companionOrigin
			if phoneOrigin == "" {
				phoneOrigin = *origin
			}
			phoneURL, err := url.Parse(phoneOrigin)
			if err != nil || phoneURL.Scheme != "https" || phoneURL.Host == "" || phoneURL.Path != "" || phoneURL.User != nil || phoneURL.Fragment != "" || phoneURL.RawQuery != "" {
				return fmt.Errorf("companion origin must be a bare HTTPS origin")
			}
			companion = service.NewCompanion(db, phoneOrigin)
			companion.HostName = lan.HostLabel()
			companion.Mode = "remote"
			hub.Companion = companion
			go companion.Maintain(ctx)
			if phoneOrigin != *origin || *companionListen != "" {
				if *companionListen == "" || *companionCert == "" || *companionKey == "" {
					return fmt.Errorf("a separate companion origin requires a TLS listener, certificate, and key")
				}
				companionServer = &http.Server{
					Addr:              *companionListen,
					Handler:           companion.PhoneHandler(),
					ReadHeaderTimeout: 4 * time.Second,
					ReadTimeout:       10 * time.Second,
					WriteTimeout:      10 * time.Second,
					IdleTimeout:       25 * time.Second,
					MaxHeaderBytes:    8 << 10,
				}
				go func() { failures <- companionServer.ListenAndServeTLS(*companionCert, *companionKey) }()
			}
		} else if workstation {
			// Automatic zero-config LAN Wi-Fi companion setup.
			addrs := lan.DetectAddresses()
			if len(addrs) > 0 {
				listenAddr := *companionListen
				if listenAddr == "" {
					listenAddr = "0.0.0.0:8444"
				}
				ln, bindErr := net.Listen("tcp", listenAddr)
				if bindErr != nil && *companionListen == "" {
					ln, bindErr = net.Listen("tcp", "0.0.0.0:0")
				}
				if bindErr != nil {
					slog.Warn("Could not start automatic Wi-Fi companion listener", "error", bindErr)
				} else {
					boundPort := ln.Addr().(*net.TCPAddr).Port
					ips := make([]string, len(addrs))
					for i, a := range addrs {
						ips[i] = a.IP
					}
					certPath, keyPath, fingerprint := *companionCert, *companionKey, ""
					var tlsErr error
					if certPath == "" || keyPath == "" {
						certPath, keyPath, fingerprint, tlsErr = lan.EnsureCompanionTLS(dir, ips)
					}
					if tlsErr != nil {
						_ = ln.Close()
						slog.Warn("Could not provision companion TLS certificate", "error", tlsErr)
					} else {
						endpoints := make([]service.CompanionEndpoint, 0, len(addrs))
						allowed := make([]string, 0, len(addrs)+1)
						for _, a := range addrs {
							epOrigin := "https://" + net.JoinHostPort(a.IP, strconv.Itoa(boundPort))
							endpoints = append(endpoints, service.CompanionEndpoint{Origin: epOrigin, Interface: a.Interface, Primary: a.Primary})
							allowed = append(allowed, epOrigin)
						}
						allowed = append(allowed, "https://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(boundPort)))
						companion = service.NewCompanion(db, endpoints[0].Origin)
						companion.Origins = endpoints
						companion.AllowedOrigins = allowed
						companion.CertFingerprint = fingerprint
						companion.Mode = "lan"
						companion.HostName = lan.HostLabel()
						hub.Companion = companion
						go companion.Maintain(ctx)
						companionServer = &http.Server{
							Handler:           companion.PhoneHandler(),
							ReadHeaderTimeout: 4 * time.Second,
							ReadTimeout:       10 * time.Second,
							WriteTimeout:      10 * time.Second,
							IdleTimeout:       25 * time.Second,
							MaxHeaderBytes:    8 << 10,
						}
						go func() { failures <- companionServer.ServeTLS(ln, certPath, keyPath) }()
						mdnsActive = lan.AdvertiseMDNS(ctx, companion.HostName, boundPort, ips, fingerprint)
					}
				}
			}
		}
	}

	tickets := &ticketStore{}
	extra := func(router *http.ServeMux) {
		if companion != nil {
			companion.ConsoleRoutes(router, auth, *origin)
			if companion.Origin == *origin && *companionListen == "" && companionServer == nil {
				companion.PhoneRoutes(router)
			}
		}
		if workstation {
			router.Handle("POST /v1/auth/launch-ticket", auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actor, _ := service.ActorFrom(r.Context())
				if !actor.CanManage() {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_, _ = w.Write([]byte(wire.Marshal(map[string]string{"ticket": tickets.create()})))
			}), *origin))
			router.HandleFunc("POST /auth/local-exchange", func(w http.ResponseWriter, r *http.Request) {
				hostPart, _, _ := net.SplitHostPort(r.RemoteAddr)
				if hostPart == "" {
					hostPart = r.RemoteAddr
				}
				if !loopback(hostPart) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				defer r.Body.Close()
				var req struct {
					Ticket string `json:"ticket"`
				}
				dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&req); err != nil || !tickets.exchange(req.Ticket) {
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"code":"INVALID_TICKET","message":"The desktop launch ticket expired or was already used."}}`))
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_, _ = w.Write([]byte(wire.Marshal(map[string]string{"token": cfg.AdminToken})))
			})
			if !*platforms.noAndroid {
				android := connector.Android{ADB: *platforms.adb}
				router.Handle("GET /v1/local/adb/discover", auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					actor, _ := service.ActorFrom(r.Context())
					if !actor.CanManage() {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					probe, stop := context.WithTimeout(r.Context(), 4*time.Second)
					defer stop()
					devs, devErr := android.Discover(probe)
					svcs, _ := android.DiscoverWireless(probe)
					if svcs == nil {
						svcs = []connector.WirelessService{}
					}
					errMsg := ""
					if devErr != nil {
						errMsg = devErr.Error()
					}
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					_, _ = w.Write([]byte(wire.Marshal(map[string]any{
						"adb_available":     devErr == nil,
						"adb_error":         errMsg,
						"wireless_services": svcs,
						"connected_count":   len(devs),
					})))
				}), *origin))
				router.Handle("POST /v1/local/adb/pair", auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					actor, _ := service.ActorFrom(r.Context())
					if !actor.CanManage() {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					defer r.Body.Close()
					var req struct {
						Address string `json:"address"`
						Code    string `json:"code"`
					}
					if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					msg, err := android.PairWireless(r.Context(), req.Address, req.Code)
					if err != nil {
						fault := wire.Public(err)
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
						w.WriteHeader(fault.Status)
						_, _ = w.Write([]byte(wire.Marshal(map[string]any{"error": fault})))
						return
					}
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					_, _ = w.Write([]byte(wire.Marshal(map[string]string{"message": msg})))
				}), *origin))
				router.Handle("POST /v1/local/adb/connect", auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					actor, _ := service.ActorFrom(r.Context())
					if !actor.CanManage() {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					defer r.Body.Close()
					var req struct {
						Address string `json:"address"`
					}
					if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					msg, err := android.ConnectWireless(r.Context(), req.Address)
					if err != nil {
						fault := wire.Public(err)
						w.Header().Set("Content-Type", "application/json; charset=utf-8")
						w.WriteHeader(fault.Status)
						_, _ = w.Write([]byte(wire.Marshal(map[string]any{"error": fault})))
						return
					}
					if hub.Local != nil {
						refreshCtx, cancelRefresh := context.WithTimeout(r.Context(), 3*time.Second)
						_ = db.Register(refreshCtx, "local", "local", hub.Local.Inventory(refreshCtx), time.Now())
						cancelRefresh()
					}
					w.Header().Set("Content-Type", "application/json; charset=utf-8")
					_, _ = w.Write([]byte(wire.Marshal(map[string]string{"message": msg})))
				}), *origin))
			}
		}
	}

	execPath := resolveExecutable()
	sysInfo := func(_ wire.Actor) map[string]any {
		mode := "remote"
		if workstation {
			mode = "workstation"
		}
		info := map[string]any{
			"version":                version,
			"mode":                   mode,
			"host_name":              lan.HostLabel(),
			"origin":                 *origin,
			"wireless_adb_supported": workstation && !*platforms.noAndroid,
			"companion_enabled":      companion != nil,
			"mdns_active":            mdnsActive,
			"mcp_executable":         execPath,
			"mcp_commands": map[string]string{
				"codex":  fmt.Sprintf("codex mcp add mobile-use -- %s mcp", quoteShellArg(execPath)),
				"claude": fmt.Sprintf("claude mcp add --transport stdio mobile-use -- %s mcp", quoteShellArg(execPath)),
				"http":   *origin + "/mcp",
			},
		}
		if workstation && !*platforms.noAndroid {
			probeCtx, stopProbe := context.WithTimeout(context.Background(), 1200*time.Millisecond)
			_, adbErr := (connector.Android{ADB: *platforms.adb}).Discover(probeCtx)
			stopProbe()
			info["adb_available"] = adbErr == nil
			if adbErr != nil {
				info["adb_error"] = adbErr.Error()
			}
		}
		if companion != nil {
			info["companion_mode"] = companion.Mode
			info["companion_origin"] = companion.Origin
			info["companion_origins"] = companion.Origins
			info["companion_fingerprint"] = companion.CertFingerprint
			info["companion_fingerprint_short"] = lan.FormatFingerprint(companion.CertFingerprint)
		}
		return info
	}

	// Bind main HTTP listener synchronously before serving so --open connects reliably.
	mainLn, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	actualOrigin := *origin
	if listenPort == "0" {
		actualPort := mainLn.Addr().(*net.TCPAddr).Port
		actualOrigin = fmt.Sprintf("%s://%s", u.Scheme, net.JoinHostPort(u.Hostname(), strconv.Itoa(actualPort)))
	}

	handler := svc.HTTP(service.HTTPOptions{
		Origin:        actualOrigin,
		Auth:          auth,
		Console:       webui.Handler(),
		MCP:           agent.HTTP(svc),
		Extra:         extra,
		Video:         video,
		LocalExchange: workstation,
		SystemInfo:    sysInfo,
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      25 * time.Second,
		IdleTimeout:       45 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	go func() {
		if u.Scheme == "https" {
			failures <- server.ServeTLS(mainLn, *httpCert, *httpKey)
		} else {
			failures <- server.Serve(mainLn)
		}
	}()

	slog.Info("mobile-use listening", "origin", actualOrigin, "version", version)
	if workstation {
		fmt.Printf("\n  Desktop console : %s\n", actualOrigin)
		if companion != nil {
			fmt.Printf("  Wi-Fi companion : %s (Fingerprint %s)\n", companion.Origin, lan.FormatFingerprint(companion.CertFingerprint))
		}
		fmt.Printf("  Pair a phone    : %s pair\n", filepath.Base(execPath))
		fmt.Printf("  Codex MCP       : codex mcp add mobile-use -- %s mcp\n", quoteShellArg(execPath))
		fmt.Printf("  Claude MCP      : claude mcp add --transport stdio mobile-use -- %s mcp\n\n", quoteShellArg(execPath))
		if shouldOpen {
			launchURL := actualOrigin + "/#launch=" + tickets.create()
			if err := openBrowser(launchURL); err != nil {
				slog.Warn("Could not open browser automatically", "error", err)
			}
		}
	}

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
	ip := net.ParseIP(strings.Trim(host, "[]"))
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
	tlsConfig, err := relay.TLS(*cert, *key, *ca, "", false)
	if err != nil {
		return err
	}
	if tlsConfig.Certificates[0].Leaf.Subject.CommonName != *name {
		return fmt.Errorf("connector certificate common name must match --name")
	}
	backends, err := platforms.backends()
	if err != nil {
		return err
	}
	return relay.Connect(ctx, *address, tlsConfig, connector.New(*space, *name, backends...))
}
