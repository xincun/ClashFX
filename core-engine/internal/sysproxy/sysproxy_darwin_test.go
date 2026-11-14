//go:build darwin

package sysproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinBackendWithFakeNetworksetup(t *testing.T) {
	cmdDir := t.TempDir()
	logFile := filepath.Join(cmdDir, "calls.log")
	stateFile := filepath.Join(cmdDir, "state.env")

	routeScript := `#!/bin/sh
echo "   route to: default"
echo "    gateway: 192.168.2.1"
echo "  interface: en0"
`
	if err := os.WriteFile(filepath.Join(cmdDir, "route"), []byte(routeScript), 0755); err != nil {
		t.Fatal(err)
	}

	nsScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
cmd="$1"
svc="$2"
case "$cmd" in
  -listnetworkserviceorder)
    cat <<'EOF'
An asterisk (*) denotes that a network service is disabled.
(1) AX88179A
(Hardware Port: AX88179A, Device: en7)

(*) Disabled USB
(Hardware Port: Disabled USB, Device: en8)

(2) Thunderbolt Bridge
(Hardware Port: Thunderbolt Bridge, Device: bridge0)

(3) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)
EOF
    ;;
  -getwebproxy|-getsecurewebproxy|-getsocksfirewallproxy)
    if [ -f %q ]; then
      . %q
      echo "Enabled: $ENABLED"
      echo "Server: $HOST"
      echo "Port: $PORT"
    else
      echo "Enabled: No"
      echo "Server: "
      echo "Port: 0"
    fi
    ;;
  -getproxybypassdomains)
    if [ -f %q.bypass ]; then
      cat %q.bypass
    else
      echo "There aren't any bypass domains set on $svc."
    fi
    ;;
  -getautoproxyurl)
    echo "URL: (null)"
    echo "Enabled: No"
    ;;
  -setwebproxy)
    printf 'ENABLED=Yes\nHOST=%%s\nPORT=%%s\n' "$3" "$4" > %q
    ;;
  -setwebproxystate)
    if [ "$3" = "off" ]; then
      printf 'ENABLED=No\nHOST=\nPORT=0\n' > %q
    fi
    ;;
  -setproxybypassdomains)
    shift 2
    : > %q.bypass
    for d in "$@"; do
      if [ "$d" = "Empty" ]; then
        rm -f %q.bypass
        break
      fi
      printf '%%s\n' "$d" >> %q.bypass
    done
    ;;
esac
`, logFile, stateFile, stateFile, stateFile, stateFile, stateFile, stateFile, stateFile, stateFile, stateFile)
	if err := os.WriteFile(filepath.Join(cmdDir, "networksetup"), []byte(nsScript), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLASHFX_DISABLE_OS_SYSPROXY", "")
	t.Setenv("CLASHFX_DARWIN_CMD_DIR", cmdDir)
	backend := NewOSBackend()
	if _, isMem := backend.(*memoryBackend); isMem {
		t.Fatal("expected *darwinBackend when CLASHFX_DARWIN_CMD_DIR is set")
	}

	mgr := NewManager(t.TempDir(), backend)
	if err := mgr.Apply(7890, "127.0.0.1,192.168.0.0/16"); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	st, err := backend.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.ProxyEnable != 1 || st.ProxyServer != "127.0.0.1:7890" || !strings.Contains(st.ProxyOverride, "192.168.0.0/16") {
		t.Fatalf("unexpected Darwin ReadState after Apply: %+v", st)
	}

	callsBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(callsBytes)
	if strings.Contains(calls, "Disabled USB") || strings.Contains(calls, "Thunderbolt Bridge") {
		t.Fatalf("disabled or bridge service should not be mutated:\n%s", calls)
	}
	if !strings.Contains(calls, "-setwebproxy Wi-Fi 127.0.0.1 7890") || !strings.Contains(calls, "-setsocksfirewallproxystate Wi-Fi on") {
		t.Fatalf("expected web+socks proxy configured on Wi-Fi:\n%s", calls)
	}

	if err := mgr.Restore(); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	restored, err := backend.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	if restored.ProxyEnable != 0 {
		t.Fatalf("expected ProxyEnable=0 after Restore, got %+v", restored)
	}
}
