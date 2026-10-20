@echo off
rem Double-click: copy the app to the desktops of the PCs listed in hosts-windows.txt
chcp 65001 >nul
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0deploy-windows.ps1" %*
pause
