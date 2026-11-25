package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mobile-use/internal/agent"
	"mobile-use/internal/client"
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
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printHelp()
		return nil
	}
	switch args[0] {
	case "init":
		return initialize()
	case "version":
		fmt.Println(version)
		return nil
	case "up":
		return serve(ctx, args[1:], true)
	case "serve":
		return serve(ctx, args[1:], false)
	case "connector":
		return connect(ctx, args[1:])
	case "ui", "console":
		return cmdUI(ctx, args[1:])
	case "pair":
		return cmdPair(ctx, args[1:])
	case "wifi":
		return cmdWifi(ctx, args[1:])
	case "devices":
		return cmdDevices(ctx, args[1:])
	case "enroll":
		return cmdEnroll(ctx, args[1:])
	case "sessions":
		return cmdSessions(ctx, args[1:])
	case "mcp":
		api, err := newClient(false)
		if err != nil {
			return err
		}
		return agent.New(agent.Remote{Client: api}).Run(ctx, &mcp.StdioTransport{})
	case "mcp-config":
		return cmdMCPConfig(args[1:])
	case "remote":
		return cmdRemote(args[1:])
	case "call":
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
		return cmdDoctor(ctx, args[1:])
	case "pki":
		return pki(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run mobile-use help", args[0])
	}
}

func printHelp() {
	fmt.Println(`mobile-use - control Android phones and iPhones from agents or the desktop console

Quick start:
  mobile-use up                         Initialize, start the daemon with Wi-Fi pairing, and open the console
  mobile-use ui                         Open the desktop web console in your browser
  mobile-use pair                       Display a QR code and pairing code for the Android companion
  mobile-use wifi scan|pair|connect     Pair and connect Android phones over Wi-Fi (Wireless Debugging)

Devices & sessions:
  mobile-use devices                    List available devices (table on terminal, JSON when piped)
  mobile-use enroll <DEVICE_ID>         Enroll, configure grants, or revoke a device
  mobile-use sessions                   List or approve/deny/end device sessions

Agents & MCP:
  mobile-use mcp                        Serve MCP over stdio (for Codex, Claude Code, Cursor, etc.)
  mobile-use mcp-config                 Print ready-to-paste Codex, Claude, and Cursor MCP commands
  mobile-use remote set|show|clear      Configure a remote HTTPS mobile-use server

Administration:
  mobile-use init                       Create local owner and agent credentials
  mobile-use serve [--help]             Run the local daemon or self-hosted server
  mobile-use connector --help           Run a remote mTLS device connector
  mobile-use token admin|agent          Print a local credential
  mobile-use doctor                     Check ADB, Wi-Fi pairing, and daemon readiness
  mobile-use call METHOD /v1/path [IN]  Call the HTTP API directly
  mobile-use pki --help                 Create a private CA or issue mTLS certificates
  mobile-use version                    Print build version`)
}

type config struct {
	AdminToken string `json:"admin_token"`
	AgentToken string `json:"agent_token"`
	DigestKey  string `json:"digest_key"`
}

type remoteProfile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
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

func ensureConfig() (config, string, bool, error) {
	dir, err := configDir()
	if err != nil {
		return config{}, "", false, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return config{}, "", false, err
	}
	path := filepath.Join(dir, "config.json")
	if existing, err := readConfig(); err == nil {
		return existing, path, false, nil
	} else if !errors.Is(err, errNotInitialized) {
		return config{}, path, false, err
	}
	value := config{AdminToken: wire.NewID() + wire.NewID(), AgentToken: wire.NewID() + wire.NewID(), DigestKey: wire.NewID() + wire.NewID()}
	if err := writeNew(path, []byte(wire.Marshal(value)), 0600); err != nil {
		if existing, rerr := readConfig(); rerr == nil {
			return existing, path, false, nil
		}
		return config{}, path, false, err
	}
	return value, path, true, nil
}

func initialize() error {
	_, path, created, err := ensureConfig()
	if err != nil {
		return err
	}
	if created {
		fmt.Println("Created credentials in", path)
	} else {
		fmt.Println("Credentials already exist in", path)
	}
	return nil
}

var errNotInitialized = errors.New("run mobile-use init or mobile-use up, or configure a remote server with mobile-use remote set")

func readConfig() (config, error) {
	var value config
	dir, err := configDir()
	if err != nil {
		return value, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return value, errNotInitialized
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

func readRemote() (remoteProfile, bool, error) {
	var profile remoteProfile
	dir, err := configDir()
	if err != nil {
		return profile, false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "remote.json"))
	if errors.Is(err, os.ErrNotExist) {
		return profile, false, nil
	}
	if err != nil {
		return profile, false, err
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return profile, false, err
	}
	if profile.URL == "" || profile.Token == "" {
		return profile, false, nil
	}
	return profile, true, nil
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

func activeOrigin() string {
	if value := os.Getenv("MOBILE_USE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	if remote, ok, err := readRemote(); err == nil && ok {
		return strings.TrimRight(remote.URL, "/")
	}
	return "http://127.0.0.1:8787"
}

func newClient(admin bool) (*client.Client, error) {
	token := os.Getenv("MOBILE_USE_TOKEN")
	origin := os.Getenv("MOBILE_USE_URL")
	if token != "" {
		if origin == "" {
			origin = "http://127.0.0.1:8787"
		}
		return client.New(origin, token)
	}
	if origin == "" {
		if remote, ok, err := readRemote(); err != nil {
			return nil, err
		} else if ok {
			return client.New(remote.URL, remote.Token)
		}
		origin = "http://127.0.0.1:8787"
	}
	cfg, err := readConfig()
	if err != nil {
		return nil, err
	}
	token = cfg.AgentToken
	if admin {
		token = cfg.AdminToken
	}
	return client.New(origin, token)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
