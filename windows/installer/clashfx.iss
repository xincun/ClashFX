#ifndef MyArch
  #define MyArch "arm64"
#endif
#ifndef AppVersion
  #define AppVersion "1.19.24"
#endif
#ifndef SourceDir
  #define SourceDir "..\dist\windows-" + MyArch
#endif
#ifndef OutputDir
  #define OutputDir "..\dist"
#endif

[Setup]
AppId={{8D6B4F77-9C35-4E31-B432-7C8F501924CF}
AppName=ClashFX
AppVersion={#AppVersion}
AppVerName=ClashFX {#AppVersion} ({#MyArch})
AppPublisher=ClashFX Project
DefaultDirName={autopf}\ClashFX
DefaultGroupName=ClashFX
DisableProgramGroupPage=yes
PrivilegesRequired=admin
Compression=lzma2/fast
SolidCompression=yes
WizardStyle=modern
SetupIconFile={#SourceDir}\resources\clashfx.ico
UninstallDisplayIcon={app}\clashfx-ui.exe
OutputDir={#OutputDir}
OutputBaseFilename=ClashFX-Setup-{#MyArch}
CloseApplications=no
#if MyArch == "arm64"
ArchitecturesAllowed=arm64
ArchitecturesInstallIn64BitMode=arm64
#else
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
#endif

[Languages]
Name: "chinesesimplified"; MessagesFile: "compiler:Default.isl,ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
chinesesimplified.CreateDesktopShortcut=创建桌面快捷方式(&D)
english.CreateDesktopShortcut=Create a &desktop shortcut
chinesesimplified.AdditionalShortcuts=附加快捷方式:
english.AdditionalShortcuts=Additional shortcuts:
chinesesimplified.RegisterServiceMsg=正在注册 ClashFX 提权 TUN 系统服务...
english.RegisterServiceMsg=Registering ClashFX privileged TUN service...
chinesesimplified.ServiceInstallFailedMsg=ClashFX 提权 TUN 系统服务安装失败 (退出码 %1)。普通系统代理模式仍可使用，您可稍后在「偏好设置」中重试安装服务。
english.ServiceInstallFailedMsg=Failed to register ClashFX privileged TUN service (exit code %1). Standard System Proxy mode still works, and you can retry installing the service later in Preferences.
chinesesimplified.InstallWebView2Msg=正在安装 Microsoft Edge WebView2 运行时...
english.InstallWebView2Msg=Installing Microsoft Edge WebView2 Runtime...
chinesesimplified.LaunchAppDesc=立即启动 ClashFX
english.LaunchAppDesc=Launch ClashFX

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopShortcut}"; GroupDescription: "{cm:AdditionalShortcuts}"; Flags: unchecked

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Excludes: "mihomo_core.exe"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "{#SourceDir}\mihomo_core.exe"; DestDir: "{app}"; DestName: "mihomo_core.bundled.exe"; Flags: ignoreversion
Source: "MicrosoftEdgeWebView2Setup.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall; Check: NeedsWebView2Runtime

[Icons]
Name: "{autoprograms}\ClashFX"; Filename: "{app}\clashfx-ui.exe"
Name: "{autodesktop}\ClashFX"; Filename: "{app}\clashfx-ui.exe"; Tasks: desktopicon

[Registry]
; clash:// URL scheme
Root: HKCR; Subkey: "clash"; ValueType: string; ValueName: ""; ValueData: "URL:ClashFX Protocol"; Flags: uninsdeletekey
Root: HKCR; Subkey: "clash"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""
Root: HKCR; Subkey: "clash\DefaultIcon"; ValueType: string; ValueName: ""; ValueData: "{app}\clashfx-ui.exe,0"
Root: HKCR; Subkey: "clash\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\clashfx-ui.exe"" ""%1"""

; clashfx:// URL scheme
Root: HKCR; Subkey: "clashfx"; ValueType: string; ValueName: ""; ValueData: "URL:ClashFX Protocol"; Flags: uninsdeletekey
Root: HKCR; Subkey: "clashfx"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""
Root: HKCR; Subkey: "clashfx\DefaultIcon"; ValueType: string; ValueName: ""; ValueData: "{app}\clashfx-ui.exe,0"
Root: HKCR; Subkey: "clashfx\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\clashfx-ui.exe"" ""%1"""

; Clean up Launch-at-Login value on uninstall if set
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "ClashFX"; Flags: uninsdeletevalue

[Run]
Filename: "{tmp}\MicrosoftEdgeWebView2Setup.exe"; Parameters: "/silent /install"; StatusMsg: "{cm:InstallWebView2Msg}"; Flags: runhidden waituntilterminated; Check: NeedsWebView2Runtime
Filename: "{app}\clashfx-ui.exe"; Description: "{cm:LaunchAppDesc}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{app}\clashfx-service.exe"; Parameters: "uninstall"; RunOnceId: "UninstallClashFXService"; Flags: runhidden waituntilterminated skipifdoesntexist

[UninstallDelete]
Type: files; Name: "{app}\mihomo_core.exe"
Type: files; Name: "{app}\mihomo_core.bundled.exe"
Type: files; Name: "{app}\mihomo_core.exe.bak*"

[Code]
const
  INTERNET_OPTION_REFRESH = 37;
  INTERNET_OPTION_SETTINGS_CHANGED = 39;

function InternetSetOptionW(hInternet: LongWord; dwOption: DWORD; lpBuffer: LongWord; dwBufferLength: DWORD): BOOL;
  external 'InternetSetOptionW@wininet.dll stdcall';

function HasValidWebView2Version(RootKey: Integer; const Subkey: String): Boolean;
var
  Pv: String;
begin
  Result := RegQueryStringValue(RootKey, Subkey, 'pv', Pv) and (Pv <> '') and (Pv <> '0.0.0.0');
end;

function NeedsWebView2Runtime(): Boolean;
var
  Wv2Subkey: String;
begin
  Wv2Subkey := 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';
  if HasValidWebView2Version(HKLM32, Wv2Subkey) or
     HasValidWebView2Version(HKLM64, Wv2Subkey) or
     HasValidWebView2Version(HKCU, Wv2Subkey) then
    Result := False
  else
    Result := True;
end;

procedure CleanupLoopbackProxyForHive(RootKey: Integer; const BaseSubkey: String);
var
  InetKey: String;
  RunKey: String;
  ProxySrv: String;
  LowerSrv: String;
begin
  InetKey := BaseSubkey + 'Software\Microsoft\Windows\CurrentVersion\Internet Settings';
  if RegQueryStringValue(RootKey, InetKey, 'ProxyServer', ProxySrv) then
  begin
    LowerSrv := LowerCase(ProxySrv);
    if (Pos('127.0.0.1:', LowerSrv) > 0) or (Pos('localhost:', LowerSrv) > 0) then
    begin
      RegWriteDWordValue(RootKey, InetKey, 'ProxyEnable', 0);
      RegDeleteValue(RootKey, InetKey, 'ProxyServer');
    end;
  end;

  RunKey := BaseSubkey + 'Software\Microsoft\Windows\CurrentVersion\Run';
  RegDeleteValue(RootKey, RunKey, 'ClashFX');
end;

procedure CleanupLoopbackProxyInRegistry();
var
  UserSids: TArrayOfString;
  I: Integer;
begin
  CleanupLoopbackProxyForHive(HKCU, '');
  if RegGetSubkeyNames(HKU, '', UserSids) then
  begin
    for I := 0 to GetArrayLength(UserSids) - 1 do
    begin
      if (Pos('S-1-5-21-', UserSids[I]) = 1) and (Pos('_Classes', UserSids[I]) = 0) then
      begin
        CleanupLoopbackProxyForHive(HKU, UserSids[I] + '\');
      end;
    end;
  end;
  InternetSetOptionW(0, INTERNET_OPTION_SETTINGS_CHANGED, 0, 0);
  InternetSetOptionW(0, INTERNET_OPTION_REFRESH, 0, 0);
end;

procedure StopRunningClashFXProcesses(ForUninstall: Boolean);
var
  ResultCode: Integer;
  SysDir: String;
  TaskkillExe: String;
  ScExe: String;
  ExistingUI: String;
  ExistingDaemon: String;
  ExistingSvc: String;
begin
  SysDir := ExpandConstant('{sys}');
  TaskkillExe := ExpandConstant('{sys}\taskkill.exe');
  ScExe := ExpandConstant('{sys}\sc.exe');

  ExistingUI := ExpandConstant('{app}\clashfx-ui.exe');
  if FileExists(ExistingUI) then
  begin
    if ForUninstall then
      Exec(ExistingUI, '--quit-uninstall', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode)
    else
      Exec(ExistingUI, '--quit', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Sleep(400);
  end;

  if ForUninstall then
  begin
    ExistingDaemon := ExpandConstant('{app}\clashfx-daemon.exe');
    if FileExists(ExistingDaemon) then
    begin
      Exec(ExistingDaemon, '-cleanup-uninstall', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode);
    end;
  end;

  // Clear SCM recovery actions before stopping/terminating so SCM will not respawn clashfx-service.exe
  Exec(ScExe, 'failure ClashFXService reset= 0 actions= ""', SysDir, SW_HIDE, ewWaitUntilTerminated, ResultCode);

  ExistingSvc := ExpandConstant('{app}\clashfx-service.exe');
  if FileExists(ExistingSvc) then
  begin
    // clashfx-service.exe stop sends SERVICE_CONTROL_STOP and waits up to 8s for STOPPED
    Exec(ExistingSvc, 'stop', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;
  if Exec(ScExe, 'stop ClashFXService', SysDir, SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0) then
  begin
    Sleep(800);
  end;

  Exec(TaskkillExe, '/F /IM clashfx-ui.exe /T', SysDir, SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(TaskkillExe, '/F /IM clashfx-daemon.exe /T', SysDir, SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(TaskkillExe, '/F /IM clashfx-service.exe /T', SysDir, SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(TaskkillExe, '/F /IM mihomo_core.exe /T', SysDir, SW_HIDE, ewWaitUntilTerminated, ResultCode);
  if ForUninstall then
  begin
    CleanupLoopbackProxyInRegistry();
  end;
  Sleep(500);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopRunningClashFXProcesses(False);
  Result := '';
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  SvcExe: String;
  BundledCore: String;
  ActiveCore: String;
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    SvcExe := ExpandConstant('{app}\clashfx-service.exe');
    if FileExists(SvcExe) then
    begin
      if (not Exec(SvcExe, 'install', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, ResultCode)) or (ResultCode <> 0) then
      begin
        Log(Format('clashfx-service.exe install failed (ResultCode=%d)', [ResultCode]));
        if not WizardSilent then
          SuppressibleMsgBox(
            FmtMessage(CustomMessage('ServiceInstallFailedMsg'), [IntToStr(ResultCode)]),
            mbError, MB_OK, IDOK);
      end;
    end;

    ActiveCore := ExpandConstant('{app}\mihomo_core.exe');
    BundledCore := ExpandConstant('{app}\mihomo_core.bundled.exe');
    if (not FileExists(ActiveCore)) and FileExists(BundledCore) then
    begin
      if not RenameFile(BundledCore, ActiveCore) then
        CopyFile(BundledCore, ActiveCore, False);
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
  begin
    StopRunningClashFXProcesses(True);
  end
  else if CurUninstallStep = usPostUninstall then
  begin
    CleanupLoopbackProxyInRegistry();
  end;
end;
