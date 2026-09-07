@echo off
setlocal
:: Cut a release:  release.bat 0.2.0
:: Runs the tests, updates CHANGELOG.md's date, commits, tags v0.2.0 and pushes.
:: GitHub Actions then builds the Windows installer + portable zip, macOS and
:: Linux packages, SHA256SUMS.txt, and publishes them on
:: https://github.com/KK4ODA/emcomm-objects/releases
:: Running apps see the new release through the in-app update check.
cd /d "%~dp0"
if "%~1"=="" (
    echo Usage: release.bat ^<version^>     e.g. release.bat 0.2.0
    exit /b 1
)
set VER=%~1
echo %VER%| findstr /r "^[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*$" >nul || (echo Version must look like 0.2.0 & exit /b 1)
git diff --quiet || (echo Working tree has uncommitted changes. Commit or stash them first. & exit /b 1)
findstr /c:"## %VER% " CHANGELOG.md >nul || findstr /c:"## %VER%" CHANGELOG.md >nul || (echo CHANGELOG.md has no "## %VER%" section. Add one first. & exit /b 1)
go vet ./... || exit /b 1
go test ./... || (echo Tests failed; release aborted. & exit /b 1)
git tag -a "v%VER%" -m "Emcomm Objects v%VER%" || exit /b 1
git push origin HEAD --follow-tags || exit /b 1
echo.
echo Tagged v%VER% and pushed. Watch the build at:
echo   https://github.com/KK4ODA/emcomm-objects/actions
echo The release appears at:
echo   https://github.com/KK4ODA/emcomm-objects/releases/tag/v%VER%
endlocal
