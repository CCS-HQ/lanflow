; Included by the Windows installer (mygo.json: windows.installerInclude).
;
; The installer finds the folder of an installed copy through its own
; uninstall entry, which is named after the app's identifier. Versions up
; to 0.1.9 used the identifier com.example.lanflow, so an installer of a
; later version would miss a copy of theirs in a folder the user chose
; and install a second one into the default folder. customInit points it
; at that folder instead, unless this version has an entry of its own or
; the folder was given on the command line (/D=).

!macro customInit
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.lanflow.app" "InstallLocation"
  StrCmp $0 "" 0 lanflow_init_done
  StrCmp $INSTDIR "$LOCALAPPDATA\Programs\Lanflow" 0 lanflow_init_done
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\com.example.lanflow" "InstallLocation"
  StrCmp $0 "" lanflow_init_done
  StrCpy $INSTDIR $0
  lanflow_init_done:
!macroend
