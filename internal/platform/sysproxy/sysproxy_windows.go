//go:build windows

package sysproxy

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

const (
	internetOptionRefresh         = 37 // INTERNET_OPTION_REFRESH
	internetOptionSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
)

var (
	wininet                = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOptionW = wininet.NewProc("InternetSetOptionW")
)

func openSettings(access uint32) (registry.Key, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, access)
	if err != nil {
		return 0, fmt.Errorf("无法打开 Internet 设置注册表项：%w", err)
	}
	return k, nil
}

// winReal is the real winSystem: WinINet and RAS (wininet_windows.go) plus
// the legacy registry values below.
type winReal struct{}

var _ winSystem = winReal{}

func set(host string, port int, bypass []string) error {
	return winSet(winReal{}, host, port, bypass)
}

func clear() error { return winClear(winReal{}) }

func setPAC(url string) error { return winSetPAC(winReal{}, url) }

// legacySet writes the manual proxy to the registry values WinINet keeps
// for the LAN. Like the other legacy* methods it is only the fallback for
// a LAN that InternetSetOptionW refused; the caller notifies.
func (winReal) legacySet(server, override string) error {
	k, err := openSettings(registry.SET_VALUE | registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("ProxyServer", server); err != nil {
		return fmt.Errorf("写入 ProxyServer 失败：%w", err)
	}
	if err := k.SetStringValue("ProxyOverride", override); err != nil {
		return fmt.Errorf("写入 ProxyOverride 失败：%w", err)
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("写入 ProxyEnable 失败：%w", err)
	}
	return nil
}

func (winReal) legacyClear() error {
	k, err := openSettings(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return fmt.Errorf("关闭系统代理失败：%w", err)
	}
	return nil
}

// legacySetPAC writes (or, for "", deletes) the AutoConfigURL value, which
// is how the registry stores the LAN's PAC address; the manual proxy
// values are left as they are.
func (winReal) legacySetPAC(url string) error {
	k, err := openSettings(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if url == "" {
		if err := k.DeleteValue("AutoConfigURL"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("关闭 PAC 代理失败：%w", err)
		}
		return nil
	}
	if err := k.SetStringValue("AutoConfigURL", url); err != nil {
		return fmt.Errorf("写入 AutoConfigURL 失败：%w", err)
	}
	return nil
}

// get reads the LAN's effective proxy settings via WinINet first (so
// PROXY_TYPE_AUTO_PROXY_URL decides PACURL accurately) and falls back to
// reading the legacy registry values when WinINet is unavailable.
func get() (State, error) {
	if c, err := (winReal{}).queryConn(winLAN); err == nil {
		st := State{
			Enabled: c.Flags&winProxyTypeProxy != 0,
			Bypass:  parseProxyOverride(c.Bypass),
		}
		st.Host, st.Port = parseProxyServer(c.Server)
		if c.Flags&winProxyTypeAutoURL != 0 {
			st.PACURL = c.AutoConfig
		}
		return st, nil
	}
	k, err := openSettings(registry.QUERY_VALUE)
	if err != nil {
		return State{}, err
	}
	defer k.Close()
	var st State
	if enable, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		st.Enabled = enable != 0
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 ProxyEnable 失败：%w", err)
	}
	if server, _, err := k.GetStringValue("ProxyServer"); err == nil {
		st.Host, st.Port = parseProxyServer(server)
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 ProxyServer 失败：%w", err)
	}
	if override, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		st.Bypass = parseProxyOverride(override)
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 ProxyOverride 失败：%w", err)
	}
	if pac, _, err := k.GetStringValue("AutoConfigURL"); err == nil {
		st.PACURL = pac
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 AutoConfigURL 失败：%w", err)
	}
	return st, nil
}

// notify tells WinINet (and through it most applications)
// that the proxy settings changed, then asks it to reload them.
func (winReal) notify() error {
	if err := procInternetSetOptionW.Find(); err != nil {
		return fmt.Errorf("系统代理已写入注册表，但无法加载 wininet.dll 通知系统刷新：%w", err)
	}
	for _, opt := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		r, _, e := procInternetSetOptionW.Call(0, opt, 0, 0)
		if r == 0 {
			return fmt.Errorf("系统代理已写入注册表，但通知系统刷新失败（选项 %d）：%v", opt, e)
		}
	}
	return nil
}
