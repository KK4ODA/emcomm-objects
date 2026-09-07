; Inno Setup script for the Emcomm Objects Windows installer.
; Built by the release workflow / packaging\build.ps1:
;   iscc /DAppVersion=0.2.0 /DSourceDir=..\dist\windows-x64 packaging\emcomm-objects.iss
; Installs per-user (no admin prompt) into %LOCALAPPDATA%\Programs\Emcomm Objects.
; Settings, objects and the log live in %LOCALAPPDATA%\EmcommObjects and
; survive upgrades and uninstalls.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\dist\windows-x64"
#endif

[Setup]
AppId={{9D2A1B7E-6C3F-4E1B-9B5A-EMCOMMOBJ001}
AppName=Emcomm Objects
AppVersion={#AppVersion}
AppVerName=Emcomm Objects {#AppVersion}
AppPublisher=KK4ODA
AppPublisherURL=https://github.com/KK4ODA/emcomm-objects
AppSupportURL=https://github.com/KK4ODA/emcomm-objects/issues
AppUpdatesURL=https://github.com/KK4ODA/emcomm-objects/releases
DefaultDirName={autopf}\Emcomm Objects
DefaultGroupName=Emcomm Objects
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\dist
OutputBaseFilename=EmcommObjects-Setup-{#AppVersion}
SetupIconFile=icon.ico
UninstallDisplayIcon={app}\emcomm-objects.exe
UninstallDisplayName=Emcomm Objects
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
LicenseFile=..\LICENSE
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Shortcuts:"
Name: "startup"; Description: "Start Emcomm Objects automatically when I log in"; GroupDescription: "Startup:"; Flags: unchecked

[Files]
Source: "{#SourceDir}\emcomm-objects.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\config.example.yaml"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\SYMBOLS-COPYRIGHT.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Emcomm Objects"; Filename: "{app}\emcomm-objects.exe"; WorkingDir: "{app}"
Name: "{group}\Uninstall Emcomm Objects"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Emcomm Objects"; Filename: "{app}\emcomm-objects.exe"; WorkingDir: "{app}"; Tasks: desktopicon
Name: "{userstartup}\Emcomm Objects"; Filename: "{app}\emcomm-objects.exe"; Parameters: "--no-browser"; WorkingDir: "{app}"; Tasks: startup

[Run]
Filename: "{app}\emcomm-objects.exe"; Description: "Start Emcomm Objects now"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; Data in %LOCALAPPDATA%\EmcommObjects is deliberately kept.
