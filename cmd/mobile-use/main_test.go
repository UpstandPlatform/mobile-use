package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureConfigIdempotent(t *testing.T) {
	t.Setenv("MOBILE_USE_CONFIG_DIR", t.TempDir())
	c1, path1, created1, err := ensureConfig()
	if err != nil || !created1 {
		t.Fatalf("first ensureConfig: created=%v err=%v", created1, err)
	}
	c2, path2, created2, err := ensureConfig()
	if err != nil || created2 || path1 != path2 {
		t.Fatalf("second ensureConfig: created=%v err=%v", created2, err)
	}
	if c1 != c2 {
		t.Fatal("credentials changed across idempotent ensureConfig calls")
	}
}

func TestRemoteProfileLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOBILE_USE_CONFIG_DIR", dir)
	if err := cmdRemote([]string{"set", "--url", "https://mobile-use.example.test/", "--token", "secret-token-1234567890"}); err != nil {
		t.Fatal(err)
	}
	profile, ok, err := readRemote()
	if err != nil || !ok || profile.URL != "https://mobile-use.example.test" {
		t.Fatalf("unexpected remote profile: %+v ok=%v err=%v", profile, ok, err)
	}
	if activeOrigin() != "https://mobile-use.example.test" {
		t.Fatalf("activeOrigin=%q", activeOrigin())
	}
	if err := cmdRemote([]string{"clear"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "remote.json")); !os.IsNotExist(err) {
		t.Fatal("remote.json should be removed on clear")
	}
}

func TestTicketStoreSingleUse(t *testing.T) {
	var store ticketStore
	ticket := store.create()
	if !store.exchange(ticket) {
		t.Fatal("first exchange should succeed")
	}
	if store.exchange(ticket) {
		t.Fatal("second exchange of same ticket must fail")
	}
	if store.exchange("nonexistent") {
		t.Fatal("unknown ticket must fail")
	}
}

func TestDoctorJSONContract(t *testing.T) {
	t.Setenv("MOBILE_USE_CONFIG_DIR", t.TempDir())
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := cmdDoctor(t.Context(), []string{"--json"})
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid doctor JSON %q: %v", buf.String(), err)
	}
	for _, key := range []string{"adb_available", "authorized_android_devices", "version", "daemon_running", "console_url"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("missing key %q in doctor output: %v", key, out)
		}
	}
}

func TestMCPConfigJSON(t *testing.T) {
	t.Setenv("MOBILE_USE_CONFIG_DIR", t.TempDir())
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := cmdMCPConfig([]string{"--json"})
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if !strings.Contains(buf.String(), "codex mcp add mobile-use") {
		t.Fatalf("missing codex command in mcp-config: %s", buf.String())
	}
}
