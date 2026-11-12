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
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Additional shortcuts:"; Flags: unchecked

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
Filename: "{app}\clashfx-service.exe"; Parameters: "install"; StatusMsg: "Registering ClashFX privileged TUN service..."; Flags: runhidden waituntilterminated
Filename: "{app}\clashfx-ui.exe"; Description: "Launch ClashFX"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{app}\clashfx-ui.exe"; Parameters: "--quit"; RunOnceId: "QuitClashFXUI"; Flags: runhidden waituntilterminated skipifdoesntexist
Filename: "{app}\clashfx-service.exe"; Parameters: "uninstall"; RunOnceId: "UninstallClashFXService"; Flags: runhidden waituntilterminated skipifdoesntexist

[Code]
procedure StopRunningClashFXProcesses();
var
  ResultCode: Integer;
  ExistingUI: String;
  ExistingSvc: String;
begin
  ExistingUI := ExpandConstant('{app}\clashfx-ui.exe');
  if FileExists(ExistingUI) then
  begin
    Exec(ExistingUI, '--quit', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Sleep(400);
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
  Sleep(500);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopRunningClashFXProcesses();
  Result := '';
end;

function InitializeUninstall(): Boolean;
begin
  StopRunningClashFXProcesses();
  Result := True;
end;
