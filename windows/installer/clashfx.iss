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
chinesesimplified.LaunchAppDesc=立即启动 ClashFX
english.LaunchAppDesc=Launch ClashFX

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopShortcut}"; GroupDescription: "{cm:AdditionalShortcuts}"; Flags: unchecked

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

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
Filename: "{app}\clashfx-service.exe"; Parameters: "install"; StatusMsg: "{cm:RegisterServiceMsg}"; Flags: runhidden waituntilterminated
Filename: "{app}\clashfx-ui.exe"; Description: "{cm:LaunchAppDesc}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{app}\clashfx-ui.exe"; Parameters: "--quit-uninstall"; RunOnceId: "QuitClashFXUI"; Flags: runhidden waituntilterminated skipifdoesntexist
Filename: "{app}\clashfx-daemon.exe"; Parameters: "-cleanup-uninstall"; RunOnceId: "CleanupClashFXDaemon"; Flags: runhidden waituntilterminated skipifdoesntexist
Filename: "{app}\clashfx-service.exe"; Parameters: "uninstall"; RunOnceId: "UninstallClashFXService"; Flags: runhidden waituntilterminated skipifdoesntexist

[Code]
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
end;

procedure StopRunningClashFXProcesses(ForUninstall: Boolean);
var
  ResultCode: Integer;
  ExistingUI: String;
  ExistingDaemon: String;
  ExistingSvc: String;
begin
  ExistingUI := ExpandConstant('{app}\clashfx-ui.exe');
  if FileExists(ExistingUI) then
  begin
    if ForUninstall then
      Exec(ExistingUI, '--quit-uninstall', '', SW_HIDE, ewWaitUntilTerminated, ResultCode)
    else
      Exec(ExistingUI, '--quit', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Sleep(400);
  end;

  if ForUninstall then
  begin
    ExistingDaemon := ExpandConstant('{app}\clashfx-daemon.exe');
    if FileExists(ExistingDaemon) then
    begin
      Exec(ExistingDaemon, '-cleanup-uninstall', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    end;
  end;

  ExistingSvc := ExpandConstant('{app}\clashfx-service.exe');
  if FileExists(ExistingSvc) then
  begin
    Exec(ExistingSvc, 'stop', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  end;

  Exec('taskkill.exe', '/F /IM clashfx-ui.exe /T', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec('taskkill.exe', '/F /IM clashfx-daemon.exe /T', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec('sc.exe', 'stop ClashFXService', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec('taskkill.exe', '/F /IM clashfx-service.exe /T', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec('taskkill.exe', '/F /IM mihomo_core.exe /T', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
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

function InitializeUninstall(): Boolean;
begin
  StopRunningClashFXProcesses(True);
  Result := True;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usPostUninstall then
  begin
    CleanupLoopbackProxyInRegistry();
  end;
end;
