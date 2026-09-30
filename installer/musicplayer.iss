; Inno Setup 6 script for the music player. Built by `scripts\build.ps1 -Installer`, which compiles the app into
; build\MusicPlayer first and passes the version (/DAppVersion) read from the executable.
;
; Installs per user by default (no administrator rights); the setup offers "Install for all users" as well.
; Registers the player under "Open with" and in Settings > Default apps for the formats in internal/formats,
; without taking over any existing default.

#ifndef AppVersion
  #define AppVersion "2.0.0"
#endif
#define AppName    "Music Player"
#define AppExe     "musicplayer.exe"
#define AppKey     "MusicPlayer"
#define SourceDir  "..\build\MusicPlayer"

[Setup]
AppId={{6F1B5C2E-8A4D-4E3B-9C7A-2D5F8E1A4B90}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=obk
AppPublisherURL=https://github.com/obk/personal-music-player-go
VersionInfoVersion={#AppVersion}
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
LicenseFile=..\LICENSE
SetupIconFile=..\cmd\musicplayer\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
OutputDir=..\build
OutputBaseFilename=MusicPlayer-{#AppVersion}-setup-x64
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
ChangesAssociations=yes
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Registry]
; ProgIDs the extensions below point to.
Root: HKA; Subkey: "Software\Classes\{#AppKey}.AudioFile"; ValueType: string; ValueData: "Audio file"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\Classes\{#AppKey}.AudioFile\DefaultIcon"; ValueType: string; ValueData: "{app}\{#AppExe},0"
Root: HKA; Subkey: "Software\Classes\{#AppKey}.AudioFile\shell\open\command"; ValueType: string; ValueData: """{app}\{#AppExe}"" ""%1"""
Root: HKA; Subkey: "Software\Classes\{#AppKey}.Playlist"; ValueType: string; ValueData: "Playlist"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\Classes\{#AppKey}.Playlist\DefaultIcon"; ValueType: string; ValueData: "{app}\{#AppExe},0"
Root: HKA; Subkey: "Software\Classes\{#AppKey}.Playlist\shell\open\command"; ValueType: string; ValueData: """{app}\{#AppExe}"" ""%1"""

; The application itself, for "Open with".
Root: HKA; Subkey: "Software\Classes\Applications\{#AppExe}"; ValueType: string; ValueName: "FriendlyAppName"; ValueData: "{#AppName}"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\Classes\Applications\{#AppExe}\shell\open\command"; ValueType: string; ValueData: """{app}\{#AppExe}"" ""%1"""

; Default apps registration (Settings > Apps > Default apps > Music Player).
Root: HKA; Subkey: "Software\{#AppKey}"; Flags: uninsdeletekeyifempty
Root: HKA; Subkey: "Software\{#AppKey}\Capabilities"; ValueType: string; ValueName: "ApplicationName"; ValueData: "{#AppName}"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\{#AppKey}\Capabilities"; ValueType: string; ValueName: "ApplicationDescription"; ValueData: "Music library and player for FLAC, MP3, WAV, OGG, M4A and AAC."
Root: HKA; Subkey: "Software\RegisteredApplications"; ValueType: string; ValueName: "{#AppName}"; ValueData: "Software\{#AppKey}\Capabilities"; Flags: uninsdeletevalue

; Per extension: listed under "Open with" and in the Default apps page, never made the default.
#define Assoc(Ext, ProgId) \
  "Root: HKA; Subkey: ""Software\Classes\." + Ext + "\OpenWithProgids""; ValueType: string; ValueName: """ + ProgId + """; ValueData: """"; Flags: uninsdeletevalue" + NewLine + \
  "Root: HKA; Subkey: ""Software\Classes\Applications\" + AppExe + "\SupportedTypes""; ValueType: string; ValueName: ""." + Ext + """; ValueData: """"" + NewLine + \
  "Root: HKA; Subkey: ""Software\" + AppKey + "\Capabilities\FileAssociations""; ValueType: string; ValueName: ""." + Ext + """; ValueData: """ + ProgId + """" + NewLine
{#Assoc("flac", AppKey + ".AudioFile")}
{#Assoc("wav",  AppKey + ".AudioFile")}
{#Assoc("mp3",  AppKey + ".AudioFile")}
{#Assoc("ogg",  AppKey + ".AudioFile")}
{#Assoc("m4a",  AppKey + ".AudioFile")}
{#Assoc("aac",  AppKey + ".AudioFile")}
{#Assoc("m3u",  AppKey + ".Playlist")}
{#Assoc("m3u8", AppKey + ".Playlist")}

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent

[Code]
// The library and settings (%AppData%\MusicPlayer) survive an uninstall unless the user asks otherwise.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Data: String;
begin
  if CurUninstallStep <> usPostUninstall then
    Exit;
  Data := ExpandConstant('{userappdata}\{#AppKey}');
  if not DirExists(Data) or UninstallSilent then
    Exit;
  if MsgBox('Also delete your library, playlists and settings?' + #13#10#13#10 + Data,
            mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    DelTree(Data, True, True, True);
end;
