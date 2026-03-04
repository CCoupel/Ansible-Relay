@echo off
REM AnsibleRelay Deployment Script for Windows
REM Usage:
REM   deploy.bat server       — Deploy relay server only
REM   deploy.bat minion       — Deploy relay minions only
REM   deploy.bat all          — Deploy both server and minions
REM   deploy.bat stop         — Stop all containers
REM   deploy.bat status       — Show status of containers

setlocal enabledelayedexpansion

set "DOCKER_HOST=tcp://192.168.1.218:2375"
set "SCRIPT_DIR=%~dp0"
set "SERVER_DIR=%SCRIPT_DIR%ansible_server"
set "MINION_DIR=%SCRIPT_DIR%ansible_minion"

if "%1"=="" (
    set "CMD=all"
) else (
    set "CMD=%1"
)

goto :%CMD%

:server
echo [*] Deploying RELAY SERVER (nats + relay-api + caddy)...
cd /d "%SERVER_DIR%"
docker compose up --build -d
echo [*] Waiting for server to be healthy...
timeout /t 15 /nobreak
echo [*] Checking health...
curl -s http://192.168.1.218:7770/health | findstr /R "ok" >nul && echo [OK] Server is healthy || echo [!] Server health unknown
exit /b 0

:minion
echo [*] Deploying RELAY MINIONS (relay-agent-01/02/03)...
cd /d "%MINION_DIR%"
docker compose up --build -d
echo [*] Waiting for minions to start...
timeout /t 10 /nobreak
echo [*] Checking agent status...
for %%i in (01 02 03) do (
    docker logs relay-agent-%%i 2>&1 | findstr /R "WebSocket connecté" >nul && echo [OK] Agent %%i connected || echo [!] Agent %%i status unknown
)
exit /b 0

:all
call :server
echo.
call :minion
exit /b 0

:stop
echo [*] Stopping all containers...
echo [*] Stopping minions...
cd /d "%MINION_DIR%"
docker compose down 2>nul
echo [*] Stopping server...
cd /d "%SERVER_DIR%"
docker compose down 2>nul
echo [OK] All containers stopped
exit /b 0

:status
echo [*] RELAY SERVER status:
cd /d "%SERVER_DIR%"
docker compose ps
echo.
echo [*] RELAY MINIONS status:
cd /d "%MINION_DIR%"
docker compose ps
exit /b 0

:help
echo AnsibleRelay Deployment Script
echo.
echo Usage:
echo     deploy.bat [COMMAND]
echo.
echo Commands:
echo     server       Deploy relay server only
echo     minion       Deploy relay minions only
echo     all          Deploy both server and minions (default)
echo     stop         Stop all containers
echo     status       Show status of all containers
echo     help         Show this help message
echo.
echo Examples:
echo     deploy.bat all
echo     deploy.bat status
echo.
exit /b 0

:default
echo Unknown command: %CMD%
call :help
exit /b 1
