@echo off
start "" /min powershell.exe -NoProfile -Command "& ([scriptblock]::Create((Get-Content -LiteralPath 'C:\OEM\test.ps1' -Raw)))"
