//go:build windows && (amd64 || arm64)

package service

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/clashfx/core-engine/internal/ailock"
	"golang.org/x/sys/windows"
)

var (
	modfwpuclnt                   = windows.NewLazySystemDLL("fwpuclnt.dll")
	procFwpmEngineOpen0           = modfwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmEngineClose0          = modfwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmTransactionBegin0     = modfwpuclnt.NewProc("FwpmTransactionBegin0")
	procFwpmTransactionCommit0    = modfwpuclnt.NewProc("FwpmTransactionCommit0")
	procFwpmTransactionAbort0     = modfwpuclnt.NewProc("FwpmTransactionAbort0")
	procFwpmProviderAdd0          = modfwpuclnt.NewProc("FwpmProviderAdd0")
	procFwpmSubLayerAdd0          = modfwpuclnt.NewProc("FwpmSubLayerAdd0")
	procFwpmFilterAdd0            = modfwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmFilterDeleteById0     = modfwpuclnt.NewProc("FwpmFilterDeleteById0")
	procFwpmGetAppIdFromFileName0 = modfwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
	procFwpmFreeMemory0           = modfwpuclnt.NewProc("FwpmFreeMemory0")

	modiphlpapi                     = windows.NewLazySystemDLL("iphlpapi.dll")
	procConvertInterfaceAliasToLuid = modiphlpapi.NewProc("ConvertInterfaceAliasToLuid")
)

// WFP Win64 ABI structures & constants verified against Windows SDK / WireGuard tunnel/firewall.
const (
	wfpRPCAuthnWinNT          uint32 = 10
	wfpSessionFlagDynamic     uint32 = 0x00000001
	wfpActionFlagTerminating  uint32 = 0x00001000
	wfpActionBlock            uint32 = 0x00000001 | wfpActionFlagTerminating
	wfpActionPermit           uint32 = 0x00000002 | wfpActionFlagTerminating
	wfpMatchEqual             uint32 = 0
	wfpMatchFlagsAllSet       uint32 = 6
	wfpCondFlagIsLoopback     uint32 = 0x00000001
	wfpDataTypeUint8          uint   = 1
	wfpDataTypeUint32         uint   = 3
	wfpDataTypeUint64         uint   = 4
	wfpDataTypeByteBlob       uint   = 12
	wfpDataTypeV4AddrMask     uint   = 0x100
	wfpDataTypeV6AddrMask     uint   = 0x101
	wfpErrFilterNotFound      uint32 = 0x80320003
)

var (
	wfpLayerALEAuthConnectV4 = windows.GUID{
		Data1: 0xc38d57d1, Data2: 0x05a7, Data3: 0x4c33,
		Data4: [8]byte{0x90, 0x4f, 0x7f, 0xbc, 0xee, 0xe6, 0x0e, 0x82},
	}
	wfpLayerALEAuthConnectV6 = windows.GUID{
		Data1: 0x4a72393b, Data2: 0x319f, Data3: 0x44bc,
		Data4: [8]byte{0x84, 0xc3, 0xba, 0x54, 0xdc, 0xb3, 0xb6, 0xb4},
	}
	wfpCondFlags = windows.GUID{
		Data1: 0x632ce23b, Data2: 0x5167, Data3: 0x435c,
		Data4: [8]byte{0x86, 0xd7, 0xe9, 0x03, 0x68, 0x4a, 0xa8, 0x0c},
	}
	wfpCondIPLocalInterface = windows.GUID{
		Data1: 0x4cd62a49, Data2: 0x59c3, Data3: 0x4969,
		Data4: [8]byte{0xb7, 0xf3, 0xbd, 0xa5, 0xd3, 0x28, 0x90, 0xa4},
	}
	wfpCondIPRemoteAddress = windows.GUID{
		Data1: 0xb235ae9a, Data2: 0x1d64, Data3: 0x49b8,
		Data4: [8]byte{0xa4, 0x4c, 0x5f, 0xf3, 0xd9, 0x09, 0x50, 0x45},
	}
	wfpCondALEAppID = windows.GUID{
		Data1: 0xd78e1e87, Data2: 0x8644, Data3: 0x4ea5,
		Data4: [8]byte{0x94, 0x37, 0xd8, 0x09, 0xec, 0xef, 0xc9, 0x71},
	}
)

type wfpByteBlob struct {
	size uint32
	data *uint8
}

type wfpDisplayData0 struct {
	name        *uint16
	description *uint16
}

type wfpValue0 struct {
	dataType uint
	value    uintptr
}

type wfpV4AddrAndMask struct {
	addr uint32
	mask uint32
}

type wfpV6AddrAndMask struct {
	addr         [16]uint8
	prefixLength uint8
}

type wfpAction0 struct {
	actionType uint32
	filterType windows.GUID
}

type wfpFilterCondition0 struct {
	fieldKey       windows.GUID
	matchType      uint32
	conditionValue wfpValue0
}

type wfpSession0 struct {
	sessionKey           windows.GUID
	displayData          wfpDisplayData0
	flags                uint32
	txnWaitTimeoutInMSec uint32
	processId            uint32
	sid                  *windows.SID
	username             *uint16
	kernelMode           uint8
}

type wfpProvider0 struct {
	providerKey  windows.GUID
	displayData  wfpDisplayData0
	flags        uint32
	providerData wfpByteBlob
	serviceName  *uint16
}

type wfpSublayer0 struct {
	subLayerKey  windows.GUID
	displayData  wfpDisplayData0
	flags        uint32
	providerKey  *windows.GUID
	providerData wfpByteBlob
	weight       uint16
}

type wfpFilter0 struct {
	filterKey           windows.GUID
	displayData         wfpDisplayData0
	flags               uint32
	providerKey         *windows.GUID
	providerData        wfpByteBlob
	layerKey            windows.GUID
	subLayerKey         windows.GUID
	weight              wfpValue0
	numFilterConditions uint32
	filterCondition     *wfpFilterCondition0
	action              wfpAction0
	padAfterAction      [4]byte
	providerContextKey  windows.GUID
	reserved            *windows.GUID
	filterID            uint64
	effectiveWeight     wfpValue0
}

func wfpCall(proc *windows.LazyProc, name string, args ...uintptr) error {
	if err := proc.Find(); err != nil {
		return fmt.Errorf("wfp %s missing: %w", name, err)
	}
	r1, _, _ := syscall.SyscallN(proc.Addr(), args...)
	if r1 != 0 {
		return fmt.Errorf("wfp %s failed: 0x%08x", name, uint32(r1))
	}
	return nil
}

func makeWFPDisplayData(name, desc string) (wfpDisplayData0, []*uint16, error) {
	nPtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return wfpDisplayData0{}, nil, err
	}
	dPtr, err := windows.UTF16PtrFromString(desc)
	if err != nil {
		return wfpDisplayData0{}, nil, err
	}
	return wfpDisplayData0{name: nPtr, description: dPtr}, []*uint16{nPtr, dPtr}, nil
}

func wfpWeight(w uint8) wfpValue0 {
	return wfpValue0{dataType: wfpDataTypeUint8, value: uintptr(w)}
}

func lookupWindowsTunLUID(alias string) (uint64, bool) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = "ClashFX"
	}
	if err := procConvertInterfaceAliasToLuid.Find(); err != nil {
		return 0, false
	}
	aliasPtr, err := windows.UTF16PtrFromString(alias)
	if err != nil {
		return 0, false
	}
	var luid uint64
	r1, _, _ := syscall.SyscallN(
		procConvertInterfaceAliasToLuid.Addr(),
		uintptr(unsafe.Pointer(aliasPtr)),
		uintptr(unsafe.Pointer(&luid)),
	)
	runtime.KeepAlive(aliasPtr)
	if r1 != 0 || luid == 0 {
		return 0, false
	}
	// Confirm the adapter is operationally UP via Go's GetAdaptersAddresses wrapper.
	iface, err := net.InterfaceByName(alias)
	if err != nil {
		return 0, false
	}
	if iface.Flags&net.FlagUp == 0 {
		return 0, false
	}
	return luid, true
}

// windowsAIGuard manages an app-scoped dynamic WFP session inside ClashFXService.
// Because FWPM_SESSION_FLAG_DYNAMIC is used, Windows automatically removes every
// filter if ClashFXService stops or uninstalls (zero risk of bricking host network).
type windowsAIGuard struct {
	mu           sync.Mutex
	session      uintptr
	providerGUID windows.GUID
	sublayerGUID windows.GUID
	filterIDs    []uint64
	lastCfg      AIGuardConfig
	status       AIGuardStatus
}

func newPlatformAIGuard() aiGuardController {
	return &windowsAIGuard{
		status: AIGuardStatus{
			Mode:    AIGuardModeInactive,
			Backend: "wfp",
		},
	}
}

func (g *windowsAIGuard) IsTunHealthy(alias string) (uint64, bool) {
	return lookupWindowsTunLUID(alias)
}

func (g *windowsAIGuard) Status() AIGuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}

func (g *windowsAIGuard) Disarm() AIGuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closeSessionLocked()
	g.lastCfg = AIGuardConfig{}
	g.status = AIGuardStatus{
		Enabled:   false,
		Armed:     false,
		Mode:      AIGuardModeInactive,
		Backend:   "wfp",
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	return g.status
}

func (g *windowsAIGuard) closeSessionLocked() {
	if g.session != 0 {
		_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", g.session)
		g.session = 0
	}
	g.filterIDs = nil
}

func openDynamicWFPSession() (uintptr, windows.GUID, windows.GUID, error) {
	disp, pins, err := makeWFPDisplayData("ClashFX AI Lock Guard", "Fail-closed dynamic WFP session for protected AI clients")
	if err != nil {
		return 0, windows.GUID{}, windows.GUID{}, err
	}
	sess := wfpSession0{
		displayData:          disp,
		flags:                wfpSessionFlagDynamic,
		txnWaitTimeoutInMSec: windows.INFINITE,
	}
	var handle uintptr
	if err := wfpCall(
		procFwpmEngineOpen0, "FwpmEngineOpen0",
		0, uintptr(wfpRPCAuthnWinNT), 0,
		uintptr(unsafe.Pointer(&sess)),
		uintptr(unsafe.Pointer(&handle)),
	); err != nil {
		return 0, windows.GUID{}, windows.GUID{}, err
	}
	runtime.KeepAlive(pins)
	runtime.KeepAlive(&sess)

	provGUID, err := windows.GenerateGUID()
	if err != nil {
		_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", handle)
		return 0, windows.GUID{}, windows.GUID{}, err
	}
	subGUID, err := windows.GenerateGUID()
	if err != nil {
		_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", handle)
		return 0, windows.GUID{}, windows.GUID{}, err
	}

	if err := wfpCall(procFwpmTransactionBegin0, "FwpmTransactionBegin0", handle, 0); err != nil {
		_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", handle)
		return 0, windows.GUID{}, windows.GUID{}, err
	}
	abortAndClose := func(cause error) (uintptr, windows.GUID, windows.GUID, error) {
		_ = wfpCall(procFwpmTransactionAbort0, "FwpmTransactionAbort0", handle)
		_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", handle)
		return 0, windows.GUID{}, windows.GUID{}, cause
	}

	pDisp, pPins, err := makeWFPDisplayData("ClashFX", "ClashFX Privileged Tunnel Service")
	if err != nil {
		return abortAndClose(err)
	}
	prov := wfpProvider0{
		providerKey: provGUID,
		displayData: pDisp,
	}
	if err := wfpCall(procFwpmProviderAdd0, "FwpmProviderAdd0", handle, uintptr(unsafe.Pointer(&prov)), 0); err != nil {
		return abortAndClose(err)
	}
	runtime.KeepAlive(pPins)
	runtime.KeepAlive(&prov)

	sDisp, sPins, err := makeWFPDisplayData("ClashFX AI Lock Filters", "Targeted fail-closed outbound guard for AI clients")
	if err != nil {
		return abortAndClose(err)
	}
	sub := wfpSublayer0{
		subLayerKey: subGUID,
		displayData: sDisp,
		providerKey: &provGUID,
		weight:      ^uint16(0),
	}
	if err := wfpCall(procFwpmSubLayerAdd0, "FwpmSubLayerAdd0", handle, uintptr(unsafe.Pointer(&sub)), 0); err != nil {
		return abortAndClose(err)
	}
	runtime.KeepAlive(sPins)
	runtime.KeepAlive(&sub)

	if err := wfpCall(procFwpmTransactionCommit0, "FwpmTransactionCommit0", handle); err != nil {
		return abortAndClose(err)
	}

	return handle, provGUID, subGUID, nil
}

func (g *windowsAIGuard) Sync(cfg AIGuardConfig, coreRunning bool) AIGuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !cfg.Enabled {
		g.closeSessionLocked()
		g.lastCfg = AIGuardConfig{}
		g.status = AIGuardStatus{
			Enabled:   false,
			Armed:     false,
			Mode:      AIGuardModeInactive,
			Backend:   "wfp",
			UpdatedAt: time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	tunAlias := strings.TrimSpace(cfg.TunDeviceName)
	if tunAlias == "" {
		tunAlias = "ClashFX"
	}

	// Merge caller-supplied executables with live service-side discovery so even at
	// boot or mid-crash newly spawned AI helpers (e.g. language_server_*.exe) are covered.
	exePaths := mergeUniqueWinPaths(cfg.ProtectedExePaths, ailock.DiscoverProtectedExecutables(cfg.ConfigDir))

	var tunLUID uint64
	tunUp := false
	if coreRunning {
		tunLUID, tunUp = lookupWindowsTunLUID(tunAlias)
	}

	var (
		newIDs          []uint64
		appliedExeCount int
		err             error
	)

	if g.session == 0 {
		newSess, provGUID, subGUID, openErr := openDynamicWFPSession()
		if openErr != nil {
			g.status = AIGuardStatus{
				Enabled:        true,
				Armed:          false,
				Mode:           AIGuardModeError,
				Backend:        "wfp",
				ProtectedCount: len(exePaths),
				TunAlias:       tunAlias,
				TunLUID:        tunLUID,
				LastError:      openErr.Error(),
				UpdatedAt:      time.Now().Format(time.RFC3339),
			}
			return g.status
		}
		newIDs, appliedExeCount, err = replaceFiltersOnSession(newSess, provGUID, subGUID, nil, exePaths, cfg.AllowLAN, cfg.AllowCIDRs, tunLUID, tunUp)
		if err != nil {
			_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", newSess)
		} else {
			g.session = newSess
			g.providerGUID = provGUID
			g.sublayerGUID = subGUID
		}
	} else {
		newIDs, appliedExeCount, err = replaceFiltersOnSession(g.session, g.providerGUID, g.sublayerGUID, g.filterIDs, exePaths, cfg.AllowLAN, cfg.AllowCIDRs, tunLUID, tunUp)
		if err != nil {
			// Fail-closed retry: open and populate a replacement dynamic session BEFORE closing
			// the existing session so active blocking filters are never dropped on transient errors.
			if newSess, provGUID, subGUID, openErr := openDynamicWFPSession(); openErr == nil {
				retryIDs, retryCount, retryErr := replaceFiltersOnSession(newSess, provGUID, subGUID, nil, exePaths, cfg.AllowLAN, cfg.AllowCIDRs, tunLUID, tunUp)
				if retryErr == nil {
					oldSess := g.session
					g.session = newSess
					g.providerGUID = provGUID
					g.sublayerGUID = subGUID
					newIDs = retryIDs
					appliedExeCount = retryCount
					err = nil
					_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", oldSess)
				} else {
					_ = wfpCall(procFwpmEngineClose0, "FwpmEngineClose0", newSess)
					err = retryErr
				}
			}
		}
	}

	if err != nil {
		// Preserve previously armed state if our existing session still holds committed filters.
		stillArmed := g.session != 0 && len(g.filterIDs) > 0
		protectedCount := appliedExeCount
		if stillArmed && g.status.ProtectedCount > 0 {
			protectedCount = g.status.ProtectedCount
		}
		g.status = AIGuardStatus{
			Enabled:        true,
			Armed:          stillArmed,
			Mode:           AIGuardModeError,
			Backend:        "wfp",
			ProtectedCount: protectedCount,
			TunAlias:       tunAlias,
			TunLUID:        tunLUID,
			LastError:      err.Error(),
			UpdatedAt:      time.Now().Format(time.RFC3339),
		}
		return g.status
	}

	g.filterIDs = newIDs
	g.lastCfg = cfg
	mode := AIGuardModeBlockingLeaks
	if coreRunning && tunUp && tunLUID != 0 {
		mode = AIGuardModeArmedNormal
	}
	g.status = AIGuardStatus{
		Enabled:        true,
		Armed:          true,
		Mode:           mode,
		Backend:        "wfp",
		ProtectedCount: appliedExeCount,
		TunAlias:       tunAlias,
		TunLUID:        tunLUID,
		UpdatedAt:      time.Now().Format(time.RFC3339),
	}
	return g.status
}

func replaceFiltersOnSession(
	session uintptr,
	providerGUID windows.GUID,
	sublayerGUID windows.GUID,
	existingFilterIDs []uint64,
	exePaths []string,
	allowLAN bool,
	allowCIDRs []string,
	tunLUID uint64,
	tunUp bool,
) ([]uint64, int, error) {
	if err := wfpCall(procFwpmTransactionBegin0, "FwpmTransactionBegin0", session, 0); err != nil {
		return nil, 0, err
	}
	abort := func(cause error) ([]uint64, int, error) {
		_ = wfpCall(procFwpmTransactionAbort0, "FwpmTransactionAbort0", session)
		return nil, 0, cause
	}

	// Delete previous filters inside the same atomic transaction (zero leak window).
	for _, id := range existingFilterIDs {
		r1, _, _ := syscall.SyscallN(procFwpmFilterDeleteById0.Addr(), session, uintptr(id))
		if r1 != 0 && uint32(r1) != wfpErrFilterNotFound {
			return abort(fmt.Errorf("wfp FwpmFilterDeleteById0(%d) failed: 0x%08x", id, uint32(r1)))
		}
	}

	var nextIDs []uint64
	addFilter := func(name string, layer windows.GUID, weight uint8, actionType uint32, conds []wfpFilterCondition0) error {
		disp, pins, err := makeWFPDisplayData(name, "")
		if err != nil {
			return err
		}
		f := wfpFilter0{
			displayData: disp,
			providerKey: &providerGUID,
			layerKey:    layer,
			subLayerKey: sublayerGUID,
			weight:      wfpWeight(weight),
			action:      wfpAction0{actionType: actionType},
		}
		if len(conds) > 0 {
			f.numFilterConditions = uint32(len(conds))
			f.filterCondition = &conds[0]
		}
		var fid uint64
		err = wfpCall(
			procFwpmFilterAdd0, "FwpmFilterAdd0",
			session,
			uintptr(unsafe.Pointer(&f)),
			0,
			uintptr(unsafe.Pointer(&fid)),
		)
		runtime.KeepAlive(pins)
		runtime.KeepAlive(conds)
		runtime.KeepAlive(&f)
		if err != nil {
			return err
		}
		nextIDs = append(nextIDs, fid)
		return nil
	}

	// 1. Weight 15: Always permit Loopback (127.0.0.0/8, ::1) so AI clients can reach local proxy & MCP servers.
	loopCond := []wfpFilterCondition0{{
		fieldKey:  wfpCondFlags,
		matchType: wfpMatchFlagsAllSet,
		conditionValue: wfpValue0{
			dataType: wfpDataTypeUint32,
			value:    uintptr(wfpCondFlagIsLoopback),
		},
	}}
	if err := addFilter("ClashFX AI Guard Permit Loopback V4", wfpLayerALEAuthConnectV4, 15, wfpActionPermit, loopCond); err != nil {
		return abort(err)
	}
	if err := addFilter("ClashFX AI Guard Permit Loopback V6", wfpLayerALEAuthConnectV6, 15, wfpActionPermit, loopCond); err != nil {
		return abort(err)
	}

	// 2. Weight 14: Permit RFC1918 private LAN & explicit TunRouteExcludes CIDRs.
	v4Allow, v6Allow := parseAllowPrefixes(allowLAN, allowCIDRs)
	for _, pfx := range v4Allow {
		am := prefixToV4AddrMask(pfx)
		cond := []wfpFilterCondition0{{
			fieldKey:  wfpCondIPRemoteAddress,
			matchType: wfpMatchEqual,
			conditionValue: wfpValue0{
				dataType: wfpDataTypeV4AddrMask,
				value:    uintptr(unsafe.Pointer(&am)),
			},
		}}
		err := addFilter("ClashFX AI Guard Permit LAN/Exclude V4 "+pfx.String(), wfpLayerALEAuthConnectV4, 14, wfpActionPermit, cond)
		runtime.KeepAlive(&am)
		if err != nil {
			return abort(err)
		}
	}
	for _, pfx := range v6Allow {
		am := prefixToV6AddrMask(pfx)
		cond := []wfpFilterCondition0{{
			fieldKey:  wfpCondIPRemoteAddress,
			matchType: wfpMatchEqual,
			conditionValue: wfpValue0{
				dataType: wfpDataTypeV6AddrMask,
				value:    uintptr(unsafe.Pointer(&am)),
			},
		}}
		err := addFilter("ClashFX AI Guard Permit LAN/Exclude V6 "+pfx.String(), wfpLayerALEAuthConnectV6, 14, wfpActionPermit, cond)
		runtime.KeepAlive(&am)
		if err != nil {
			return abort(err)
		}
	}

	// 3. Weight 12: Permit outbound connections routed through the live ClashFX Wintun adapter ONLY.
	if tunUp && tunLUID != 0 {
		luidVal := tunLUID
		tunCond := []wfpFilterCondition0{{
			fieldKey:  wfpCondIPLocalInterface,
			matchType: wfpMatchEqual,
			conditionValue: wfpValue0{
				dataType: wfpDataTypeUint64,
				value:    uintptr(unsafe.Pointer(&luidVal)),
			},
		}}
		err4 := addFilter("ClashFX AI Guard Permit TUN V4", wfpLayerALEAuthConnectV4, 12, wfpActionPermit, tunCond)
		err6 := addFilter("ClashFX AI Guard Permit TUN V6", wfpLayerALEAuthConnectV6, 12, wfpActionPermit, tunCond)
		runtime.KeepAlive(&luidVal)
		if err4 != nil {
			return abort(err4)
		}
		if err6 != nil {
			return abort(err6)
		}
	}

	// 4. Weight 1: Block all other outbound connections for each protected AI executable.
	// Non-AI apps (DingTalk, WeChat, Chrome, corporate tools) are never matched and pass untouched.
	appliedApps := 0
	for _, exePath := range exePaths {
		if _, statErr := os.Stat(exePath); statErr != nil {
			continue
		}
		pathPtr, err := windows.UTF16PtrFromString(exePath)
		if err != nil {
			continue
		}
		var appID *wfpByteBlob
		err = wfpCall(
			procFwpmGetAppIdFromFileName0, "FwpmGetAppIdFromFileName0",
			uintptr(unsafe.Pointer(pathPtr)),
			uintptr(unsafe.Pointer(&appID)),
		)
		runtime.KeepAlive(pathPtr)
		if err != nil || appID == nil {
			continue
		}
		cond := []wfpFilterCondition0{{
			fieldKey:  wfpCondALEAppID,
			matchType: wfpMatchEqual,
			conditionValue: wfpValue0{
				dataType: wfpDataTypeByteBlob,
				value:    uintptr(unsafe.Pointer(appID)),
			},
		}}
		err4 := addFilter("ClashFX AI Guard Block Direct V4", wfpLayerALEAuthConnectV4, 1, wfpActionBlock, cond)
		err6 := addFilter("ClashFX AI Guard Block Direct V6", wfpLayerALEAuthConnectV6, 1, wfpActionBlock, cond)
		runtime.KeepAlive(appID)
		_ = wfpCall(procFwpmFreeMemory0, "FwpmFreeMemory0", uintptr(unsafe.Pointer(&appID)))
		if err4 != nil {
			return abort(err4)
		}
		if err6 != nil {
			return abort(err6)
		}
		appliedApps++
	}

	if err := wfpCall(procFwpmTransactionCommit0, "FwpmTransactionCommit0", session); err != nil {
		return abort(err)
	}
	return nextIDs, appliedApps, nil
}

func prefixToV4AddrMask(pfx netip.Prefix) wfpV4AddrAndMask {
	m := pfx.Masked()
	bits := m.Bits()
	var mask uint32
	if bits > 0 {
		mask = ^uint32(0) << uint(32-bits)
	}
	return wfpV4AddrAndMask{
		addr: binary.BigEndian.Uint32(m.Addr().AsSlice()),
		mask: mask,
	}
}

func prefixToV6AddrMask(pfx netip.Prefix) wfpV6AddrAndMask {
	m := pfx.Masked()
	return wfpV6AddrAndMask{
		addr:         m.Addr().As16(),
		prefixLength: uint8(m.Bits()),
	}
}

func mergeUniqueWinPaths(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, raw := range list {
			p := strings.TrimSpace(strings.ReplaceAll(raw, "/", `\`))
			if p == "" {
				continue
			}
			key := strings.ToLower(p)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}
