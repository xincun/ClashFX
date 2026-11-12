//go:build windows

package sysproxy

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsKeyPath          = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	internetOptionRefresh            = 37
	internetOptionSettingsChanged    = 39
)

var (
	modwininet               = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOptionW   = modwininet.NewProc("InternetSetOptionW")
)

type windowsRegistryBackend struct{}

func NewOSBackend() Backend {
	return windowsRegistryBackend{}
}

func (windowsRegistryBackend) ReadState() (OSProxyState, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return OSProxyState{}, fmt.Errorf("open HKCU Internet Settings: %w", err)
	}
	defer k.Close()

	var st OSProxyState
	if val, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		st.ProxyEnable = uint32(val)
	} else if !errors.Is(err, registry.ErrNotExist) {
		return st, fmt.Errorf("read ProxyEnable: %w", err)
	}

	if s, _, err := k.GetStringValue("ProxyServer"); err == nil {
		st.HasProxyServer = true
		st.ProxyServer = s
	} else if !errors.Is(err, registry.ErrNotExist) {
		return st, fmt.Errorf("read ProxyServer: %w", err)
	}

	if s, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		st.HasProxyOverride = true
		st.ProxyOverride = s
	} else if !errors.Is(err, registry.ErrNotExist) {
		return st, fmt.Errorf("read ProxyOverride: %w", err)
	}

	if s, _, err := k.GetStringValue("AutoConfigURL"); err == nil {
		st.HasAutoConfigURL = true
		st.AutoConfigURL = s
	} else if !errors.Is(err, registry.ErrNotExist) {
		return st, fmt.Errorf("read AutoConfigURL: %w", err)
	}

	return st, nil
}

func (windowsRegistryBackend) WriteState(st OSProxyState) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU Internet Settings for write: %w", err)
	}
	defer k.Close()

	if err := k.SetDWordValue("ProxyEnable", st.ProxyEnable); err != nil {
		return fmt.Errorf("write ProxyEnable: %w", err)
	}

	if st.HasProxyServer {
		if err := k.SetStringValue("ProxyServer", st.ProxyServer); err != nil {
			return fmt.Errorf("write ProxyServer: %w", err)
		}
	} else {
		_ = k.DeleteValue("ProxyServer")
	}

	if st.HasProxyOverride {
		if err := k.SetStringValue("ProxyOverride", st.ProxyOverride); err != nil {
			return fmt.Errorf("write ProxyOverride: %w", err)
		}
	} else {
		_ = k.DeleteValue("ProxyOverride")
	}

	if st.HasAutoConfigURL {
		if err := k.SetStringValue("AutoConfigURL", st.AutoConfigURL); err != nil {
			return fmt.Errorf("write AutoConfigURL: %w", err)
		}
	} else {
		_ = k.DeleteValue("AutoConfigURL")
	}

	return notifyWinINetRefresh()
}

func notifyWinINetRefresh() error {
	if err := modwininet.Load(); err != nil {
		return fmt.Errorf("load wininet.dll: %w", err)
	}
	if err := procInternetSetOptionW.Find(); err != nil {
		return fmt.Errorf("find InternetSetOptionW: %w", err)
	}
	r1, _, e1 := procInternetSetOptionW.Call(0, uintptr(internetOptionSettingsChanged), 0, 0)
	if r1 == 0 {
		return fmt.Errorf("InternetSetOptionW(SETTINGS_CHANGED): %w", e1)
	}
	r2, _, e2 := procInternetSetOptionW.Call(0, uintptr(internetOptionRefresh), 0, 0)
	if r2 == 0 {
		return fmt.Errorf("InternetSetOptionW(REFRESH): %w", e2)
	}
	return nil
}
