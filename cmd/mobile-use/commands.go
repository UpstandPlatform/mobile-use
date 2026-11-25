package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"mobile-use/internal/client"
	"mobile-use/internal/connector"
	"mobile-use/internal/lan"
	"mobile-use/internal/qr"
	"mobile-use/internal/wire"
)

func resolveExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return "mobile-use"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

func quoteShellArg(arg string) string {
	if !strings.ContainsAny(arg, " \t'\"") {
		return arg
	}
	return `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
}

func cmdUI(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("ui", flag.ContinueOnError)
	printOnly := f.Bool("print", false, "Print the console URL without opening a browser")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	origin := activeOrigin()
	launchURL := origin
	if _, ok, _ := readRemote(); !ok && os.Getenv("MOBILE_USE_TOKEN") == "" && os.Getenv("MOBILE_USE_URL") == "" {
		api, err := newClient(true)
		if err != nil {
			return err
		}
		reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		if err := api.Request(reqCtx, "POST", "/v1/auth/launch-ticket", nil, &ticket); err != nil {
			return fmt.Errorf("local daemon is not reachable at %s (%v); start it with `mobile-use up`", origin, err)
		}
		if ticket.Ticket != "" {
			launchURL = origin + "/#launch=" + ticket.Ticket
		}
	}
	if *printOnly {
		fmt.Println(launchURL)
		return nil
	}
	if err := openBrowser(launchURL); err != nil {
		fmt.Println("Open this URL in your browser:", launchURL)
		return nil
	}
	fmt.Println("Opened desktop console:", origin)
	return nil
}

func cmdPair(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("pair", flag.ContinueOnError)
	pairOrigin := f.String("origin", "", "Specific server origin for the companion to reach")
	autoEnroll := f.Bool("auto-enroll", true, "Automatically enroll the device for local MCP agent access")
	wait := f.Bool("wait", true, "Wait until the phone finishes pairing")
	noWait := f.Bool("no-wait", false, "Print the pairing code/QR and exit immediately")
	noQR := f.Bool("no-qr", false, "Do not render an ASCII/Unicode QR code")
	jsonOut := f.Bool("json", false, "Print raw JSON response")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	api, err := newClient(true)
	if err != nil {
		return err
	}
	payload := map[string]any{"auto_enroll": *autoEnroll}
	if *pairOrigin != "" {
		payload["origin"] = *pairOrigin
	}
	var created struct {
		ID          string `json:"id"`
		Code        string `json:"code"`
		Origin      string `json:"origin"`
		Fingerprint string `json:"fingerprint"`
		Mode        string `json:"mode"`
		HostName    string `json:"host_name"`
		QRURI       string `json:"qr_uri"`
		ExpiresAt   string `json:"expires_at"`
		AutoEnroll  bool   `json:"auto_enroll"`
	}
	if err := api.Request(ctx, "POST", "/v1/companion/pairing", payload, &created); err != nil {
		return err
	}
	if *jsonOut {
		fmt.Println(wire.Marshal(created))
		return nil
	}
	fmt.Println()
	if !*noQR {
		if code, err := qr.Encode(created.QRURI); err == nil {
			fmt.Print(code.Terminal(isTerminal(os.Stdout)))
			fmt.Println()
		}
	}
	fmt.Printf("  Pairing code   : %s\n", created.Code)
	fmt.Printf("  Server address : %s\n", created.Origin)
	if created.Fingerprint != "" {
		fmt.Printf("  Fingerprint    : %s\n", lan.FormatFingerprint(created.Fingerprint))
	}
	if exp, err := time.Parse(time.RFC3339Nano, created.ExpiresAt); err == nil {
		fmt.Printf("  Expires at     : %s\n", exp.Local().Format("15:04:05"))
	}
	fmt.Println("  Scan the QR code in the mobile-use Android companion app, or select Nearby on Wi-Fi.")
	if !*wait || *noWait {
		fmt.Println()
		return nil
	}
	fmt.Println("  Waiting for phone confirmation (Ctrl+C to cancel)...")
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			var status struct {
				State        string `json:"state"`
				DeviceID     string `json:"device_id"`
				Label        string `json:"label"`
				AutoEnrolled bool   `json:"auto_enrolled"`
			}
			if err := api.Request(ctx, "GET", "/v1/companion/pairing/"+created.ID, nil, &status); err != nil {
				return err
			}
			switch status.State {
			case "paired":
				fmt.Printf("\n  Paired: %s (%s)\n", status.Label, status.DeviceID)
				if status.AutoEnrolled {
					fmt.Println("  Enrolled and ready for MCP agent sessions.")
				} else {
					fmt.Printf("  Run `mobile-use enroll %s` or open the console to finish enrollment.\n", status.DeviceID)
				}
				return nil
			case "expired":
				return fmt.Errorf("pairing code expired before a phone enrolled")
			}
		}
	}
}

func cmdWifi(ctx context.Context, args []string) error {
	adb := connector.Android{ADB: os.Getenv("MOBILE_USE_ADB")}
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println(`usage:
  mobile-use wifi scan [--json]
  mobile-use wifi pair <HOST:PAIRING_PORT> <6-DIGIT-CODE>
  mobile-use wifi connect <HOST:DEBUG_PORT>`)
		return nil
	}
	switch args[0] {
	case "scan":
		f := flag.NewFlagSet("wifi scan", flag.ContinueOnError)
		jsonOut := f.Bool("json", false, "Output JSON")
		if err := f.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		services, err := adb.DiscoverWireless(ctx)
		if err != nil {
			return err
		}
		if *jsonOut || !isTerminal(os.Stdout) {
			if services == nil {
				services = []connector.WirelessService{}
			}
			fmt.Println(wire.Marshal(services))
			return nil
		}
		if len(services) == 0 {
			fmt.Println("No Android Wireless Debugging endpoints found via mDNS.")
			fmt.Println("On the phone: Settings -> Developer options -> Wireless debugging -> Pair device with pairing code.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "TYPE\tADDRESS\tSERVICE")
		for _, svc := range services {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", svc.Type, svc.Address, svc.Name)
		}
		return tw.Flush()
	case "pair":
		if len(args) != 3 {
			return fmt.Errorf("usage: mobile-use wifi pair <HOST:PAIRING_PORT> <6-DIGIT-CODE>")
		}
		msg, err := adb.PairWireless(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	case "connect":
		if len(args) != 2 {
			return fmt.Errorf("usage: mobile-use wifi connect <HOST:DEBUG_PORT>")
		}
		msg, err := adb.ConnectWireless(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	default:
		return fmt.Errorf("unknown wifi subcommand %q; run mobile-use wifi --help", args[0])
	}
}

func cmdDevices(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("devices", flag.ContinueOnError)
	jsonOut := f.Bool("json", false, "Output raw JSON")
	asRole := f.String("as", "admin", "Local identity when using local config (admin or agent)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	admin := *asRole != "agent"
	api, err := newClient(admin)
	if err != nil {
		return err
	}
	var devices []wire.Device
	if err := api.Request(ctx, "GET", "/v1/devices", nil, &devices); err != nil {
		return err
	}
	if *jsonOut || !isTerminal(os.Stdout) {
		fmt.Println(wire.Marshal(devices))
		return nil
	}
	if len(devices) == 0 {
		fmt.Println("No devices found. Connect a phone via USB/Wi-Fi ADB, or run `mobile-use pair`.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLABEL\tPLATFORM\tMODE\tSTATUS\tPROFILE\tGRANTS")
	for _, d := range devices {
		status := "offline"
		if d.Online {
			status = "online"
		}
		profile := d.Profile
		if !d.Enabled {
			profile = "unenrolled"
		}
		var grants []string
		for _, g := range d.Grants {
			grants = append(grants, g.Subject)
		}
		grantStr := "-"
		if len(grants) > 0 {
			grantStr = strings.Join(grants, ",")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Label, d.Platform, d.Mode, status, profile, grantStr)
	}
	return tw.Flush()
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func cmdEnroll(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("enroll", flag.ContinueOnError)
	label := f.String("label", "", "Display label for the device")
	profile := f.String("profile", wire.Personal, "Access profile: personal or dedicated")
	owner := f.String("owner", "local-owner", "Owner subject identity")
	revoke := f.Bool("revoke", false, "Revoke enrollment for this device")
	jsonOut := f.Bool("json", false, "Output JSON")
	var grantsFlag stringList
	f.Var(&grantsFlag, "grant", "Agent grant as subject=cap1,cap2 (repeatable)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: mobile-use enroll <DEVICE_ID> [--profile personal|dedicated] [--grant subject=cap1,cap2]")
	}
	deviceID := f.Arg(0)
	api, err := newClient(true)
	if err != nil {
		return err
	}
	var current wire.Device
	if err := api.Request(ctx, "GET", "/v1/devices/"+deviceID, nil, &current); err != nil {
		return err
	}
	deviceLabel := *label
	if deviceLabel == "" {
		deviceLabel = current.Label
	}
	var grants []wire.Grant
	for _, entry := range grantsFlag {
		subj, rawCaps, hasEq := strings.Cut(entry, "=")
		subj = strings.TrimSpace(subj)
		var caps []wire.Capability
		if hasEq {
			for _, part := range strings.Split(rawCaps, ",") {
				if c := wire.Capability(strings.TrimSpace(part)); c != "" {
					caps = append(caps, c)
				}
			}
		} else {
			for c, ok := range current.Capabilities {
				if ok {
					caps = append(caps, c)
				}
			}
		}
		grants = append(grants, wire.Grant{Subject: subj, Capabilities: caps})
	}
	if len(grants) == 0 && !*revoke {
		if len(current.Grants) > 0 {
			grants = current.Grants
		} else {
			var caps []wire.Capability
			for c, ok := range current.Capabilities {
				if ok {
					caps = append(caps, c)
				}
			}
			grants = []wire.Grant{{Subject: "local-agent", Capabilities: caps}}
		}
	}
	enabled := !*revoke
	req := wire.Enrollment{
		Label:   deviceLabel,
		Owner:   *owner,
		Profile: *profile,
		Grants:  grants,
		Enabled: &enabled,
	}
	var updated wire.Device
	if err := api.Request(ctx, "PUT", "/v1/devices/"+deviceID, req, &updated); err != nil {
		return err
	}
	if *jsonOut || !isTerminal(os.Stdout) {
		fmt.Println(wire.Marshal(updated))
		return nil
	}
	if *revoke {
		fmt.Printf("Revoked enrollment for %s (%s)\n", updated.Label, updated.ID)
	} else {
		fmt.Printf("Enrolled %s (%s) as %s\n", updated.Label, updated.ID, updated.Profile)
	}
	return nil
}

func cmdSessions(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "approve" || args[0] == "deny" || args[0] == "end") {
		if len(args) != 2 {
			return fmt.Errorf("usage: mobile-use sessions %s <SESSION_ID>", args[0])
		}
		api, err := newClient(true)
		if err != nil {
			return err
		}
		var updated wire.Session
		if err := api.Request(ctx, "POST", "/v1/sessions/"+args[1]+"/"+args[0], nil, &updated); err != nil {
			return err
		}
		fmt.Printf("Session %s is now %s\n", updated.ID, updated.State)
		return nil
	}
	f := flag.NewFlagSet("sessions", flag.ContinueOnError)
	jsonOut := f.Bool("json", false, "Output raw JSON")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	api, err := newClient(true)
	if err != nil {
		return err
	}
	var sessions []wire.Session
	if err := api.Request(ctx, "GET", "/v1/sessions", nil, &sessions); err != nil {
		return err
	}
	if *jsonOut || !isTerminal(os.Stdout) {
		fmt.Println(wire.Marshal(sessions))
		return nil
	}
	if len(sessions) == 0 {
		fmt.Println("No pending or active sessions.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDEVICE\tSTATE\tREQUESTER\tPURPOSE")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.DeviceID, s.State, s.Requester, s.Purpose)
	}
	return tw.Flush()
}

func cmdMCPConfig(args []string) error {
	f := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	target := f.String("client", "all", "Target client: all, codex, claude, claude-desktop, cursor, http")
	jsonOut := f.Bool("json", false, "Output JSON")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	execPath := resolveExecutable()
	quoted := quoteShellArg(execPath)
	remote, hasRemote, _ := readRemote()
	codexCmd := fmt.Sprintf("codex mcp add mobile-use -- %s mcp", quoted)
	claudeCmd := fmt.Sprintf("claude mcp add --transport stdio mobile-use -- %s mcp", quoted)
	serverObj := map[string]any{
		"command": execPath,
		"args":    []string{"mcp"},
	}
	if hasRemote {
		serverObj["env"] = map[string]string{
			"MOBILE_USE_URL":   remote.URL,
			"MOBILE_USE_TOKEN": remote.Token,
		}
	}
	desktopJSON := map[string]any{
		"mcpServers": map[string]any{
			"mobile-use": serverObj,
		},
	}
	httpURL := activeOrigin() + "/mcp"

	if *jsonOut {
		fmt.Println(wire.Marshal(map[string]any{
			"executable":     execPath,
			"codex":          codexCmd,
			"claude":         claudeCmd,
			"claude_desktop": desktopJSON,
			"http_endpoint":  httpURL,
		}))
		return nil
	}

	switch *target {
	case "codex":
		fmt.Println(codexCmd)
	case "claude":
		fmt.Println(claudeCmd)
	case "claude-desktop", "cursor":
		raw, _ := json.MarshalIndent(desktopJSON, "", "  ")
		fmt.Println(string(raw))
	case "http":
		fmt.Println(httpURL)
	case "all":
		fmt.Println("Codex CLI:")
		fmt.Println(" ", codexCmd)
		fmt.Println("\nClaude Code:")
		fmt.Println(" ", claudeCmd)
		fmt.Println("\nClaude Desktop / Cursor (mcpServers JSON):")
		raw, _ := json.MarshalIndent(desktopJSON, "", "  ")
		fmt.Println(string(raw))
		fmt.Println("\nRemote Streamable HTTP MCP:")
		fmt.Println(" ", httpURL)
	default:
		return fmt.Errorf("unknown client %q", *target)
	}
	return nil
}

func cmdRemote(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Println(`usage:
  mobile-use remote set --url https://... --token <ACCESS_TOKEN>
  mobile-use remote show
  mobile-use remote clear`)
		return nil
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "remote.json")
	switch args[0] {
	case "set":
		f := flag.NewFlagSet("remote set", flag.ContinueOnError)
		remoteURL := f.String("url", "", "Remote HTTPS origin (for example https://mobile-use.example.com)")
		token := f.String("token", "", "Remote API bearer token")
		if err := f.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		cleanURL := strings.TrimRight(strings.TrimSpace(*remoteURL), "/")
		if _, err := client.New(cleanURL, *token); err != nil {
			return err
		}
		if *token == "" {
			return fmt.Errorf("provide --token")
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(wire.Marshal(remoteProfile{URL: cleanURL, Token: *token})), 0600); err != nil {
			return err
		}
		fmt.Println("Saved remote server profile:", cleanURL)
		return nil
	case "show":
		profile, ok, err := readRemote()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Using local workstation mode (no remote profile configured).")
			return nil
		}
		fmt.Printf("Remote server : %s\nToken prefix  : %s...\n", profile.URL, profile.Token[:min(8, len(profile.Token))])
		return nil
	case "clear":
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("Cleared remote profile. Reverted to local workstation mode.")
		return nil
	default:
		return fmt.Errorf("unknown remote subcommand %q", args[0])
	}
}

func cmdDoctor(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := f.Bool("json", false, "Output JSON")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	probe, stop := context.WithTimeout(ctx, 6*time.Second)
	defer stop()
	android := connector.Android{ADB: os.Getenv("MOBILE_USE_ADB")}
	devices, err := android.Discover(probe)
	wireless, _ := android.DiscoverWireless(probe)
	addrs := lan.DetectAddresses()
	lanIPs := make([]string, len(addrs))
	for i, a := range addrs {
		lanIPs[i] = a.IP
	}

	daemonRunning := false
	companionOrigin := ""
	if api, clientErr := newClient(true); clientErr == nil {
		var info map[string]any
		if api.Request(probe, "GET", "/v1/system/info", nil, &info) == nil {
			daemonRunning = true
			if v, ok := info["companion_origin"].(string); ok {
				companionOrigin = v
			}
		}
	}

	result := map[string]any{
		"adb_available":              err == nil,
		"authorized_android_devices": len(devices),
		"wireless_adb_services":      len(wireless),
		"lan_addresses":              lanIPs,
		"daemon_running":             daemonRunning,
		"console_url":                activeOrigin(),
		"version":                    version,
	}
	if companionOrigin != "" {
		result["companion_origin"] = companionOrigin
	}
	if err != nil {
		result["adb_error"] = err.Error()
	}
	if *jsonOut || !isTerminal(os.Stdout) {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	mark := func(ok bool) string {
		if ok {
			return "[OK]"
		}
		return "[--]"
	}
	fmt.Printf("mobile-use %s\n", version)
	fmt.Printf("  %s Daemon reachable at %s\n", mark(daemonRunning), activeOrigin())
	if companionOrigin != "" {
		fmt.Printf("  [OK] Wi-Fi companion at %s\n", companionOrigin)
	}
	fmt.Printf("  %s ADB available (%d authorized USB/Wi-Fi devices, %d mDNS wireless services)\n", mark(err == nil), len(devices), len(wireless))
	if err != nil {
		fmt.Printf("       ADB detail: %s\n", err.Error())
	}
	fmt.Printf("  %s LAN IPv4 addresses: %s\n", mark(len(lanIPs) > 0), strings.Join(lanIPs, ", "))
	return nil
}

func call(ctx context.Context, args []string) error {
	api, err := newClient(false)
	if err != nil {
		return err
	}
	if len(args) < 3 || len(args) > 4 {
		return fmt.Errorf("usage: mobile-use call METHOD /v1/path [FILE|-]")
	}
	method, path := args[1], args[2]
	var body json.RawMessage
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
