Unicode true
Name "Filecloud"
OutFile "$%FILECLOUD_SETUP%"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\Filecloud"
Icon "$%FILECLOUD_STAGE%\filecloud.ico"
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles
Function .onInit
 SetRegView 32
 ReadRegStr $0 HKLM "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
 StrCmp $0 "" 0 ready
 ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
 StrCmp $0 "" 0 ready
 MessageBox MB_OK "Install Microsoft Edge WebView2 Evergreen Runtime before running Filecloud: https://developer.microsoft.com/microsoft-edge/webview2/" /SD IDOK
 ready:
FunctionEnd
Section "Install"
 SetShellVarContext current
 SetOutPath "$INSTDIR"
 ; Remove the old companion only when upgrading an existing desktop installation.
 IfFileExists "$INSTDIR\filecloud-desktop.exe" 0 +2
 Delete "$INSTDIR\filecloud.exe"
 File /r "$%FILECLOUD_STAGE%\*"
 WriteUninstaller "$INSTDIR\Uninstall.exe"
 CreateShortcut "$SMPROGRAMS\Filecloud.lnk" "$INSTDIR\filecloud-desktop.exe" "" "$INSTDIR\filecloud.ico"
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\FilecloudDesktop" "DisplayName" "Filecloud Desktop"
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\FilecloudDesktop" "UninstallString" '$\"$INSTDIR\Uninstall.exe$\"'
SectionEnd
Section "Uninstall"
 SetShellVarContext current
 Delete "$SMPROGRAMS\Filecloud.lnk"
 Delete "$INSTDIR\filecloud-client.exe"
 Delete "$INSTDIR\filecloud-desktop.exe"
 Delete "$INSTDIR\filecloud.ico"
 Delete "$INSTDIR\Uninstall.exe"
 RMDir /r "$INSTDIR\licenses"
 RMDir "$INSTDIR"
 DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\FilecloudDesktop"
SectionEnd
