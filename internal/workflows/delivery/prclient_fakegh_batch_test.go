package delivery

import "strings"

// fakeGHScriptWindows returns the batch translation of the fake gh double
// (see writeFakeGH for the shared contract). Windows cannot execute POSIX
// shell scripts, and an extensionless "gh" file is invisible to
// exec.LookPath there (PATHEXT only adds .COM/.EXE/.BAT/.CMD), which used to
// make these tests call the REAL gh and reach GitHub. cmd.exe runs the same
// double as a batch file, resolved through PATHEXT as gh.cmd; argv is
// recorded space-joined (echo %*) and readRecordedFileArgs splits it back
// with a quote-aware splitter on Windows.
func fakeGHScriptWindows() string {
	scheme := `@echo off
setlocal
echo %* | findstr /i "baseRefOid" >nul
if %ERRORLEVEL%==0 goto reject
if "%1"=="api" goto api
if defined GH_ARGS_FILE echo %* > "%GH_ARGS_FILE%"
if defined GH_ENV_FILE set > "%GH_ENV_FILE%"
if defined GH_EXIT goto ghexit
if defined GH_STDOUT echo %GH_STDOUT%
exit /b 0

:ghexit
if defined GH_EXIT_MSG (echo %GH_EXIT_MSG% 1>&2) else (echo gh failed 1>&2)
exit /b %GH_EXIT%

:reject
echo Unknown JSON field: "baseRefOid" 1>&2
echo Available fields: 1>&2
echo   additions 1>&2
exit /b 1

:api
set /a cnt=0
for %%a in (%*) do set /a cnt+=1
if %cnt% LSS 2 goto apifew
if %cnt% GTR 2 goto apimany
if defined GH_API_ARGS_FILE echo %* > "%GH_API_ARGS_FILE%"
if defined GH_API_EXIT goto apiexit
if defined GH_STDOUT_API (echo %GH_STDOUT_API%) else (echo {"base":{"sha":"1111111111111111111111111111111111111111"}})
exit /b 0

:apifew
echo accepts 1 arg(s), received 0 1>&2
exit /b 1

:apimany
set /a received=%cnt%-1
echo accepts 1 arg(s), received %received% 1>&2
exit /b 1

:apiexit
if defined GH_API_EXIT_MSG (echo %GH_API_EXIT_MSG% 1>&2) else (echo gh api failed 1>&2)
exit /b %GH_API_EXIT%
`
	return strings.ReplaceAll(scheme, "\n", "\r\n")
}
