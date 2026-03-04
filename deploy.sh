#!/bin/bash
# AnsibleRelay Deployment Script
# Usage:
#   ./deploy.sh server       — Deploy relay server only
#   ./deploy.sh minion       — Deploy relay minions only
#   ./deploy.sh all          — Deploy both server and minions
#   ./deploy.sh stop         — Stop all containers
#   ./deploy.sh status       — Show status of containers

set -e

DOCKER_HOST="${DOCKER_HOST:-tcp://192.168.1.218:2375}"
export DOCKER_HOST

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$SCRIPT_DIR/ansible_server"
MINION_DIR="$SCRIPT_DIR/ansible_minion"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[*]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[!]${NC} $1"
}

log_error() {
    echo -e "${RED}[x]${NC} $1"
}

deploy_server() {
    log_info "Deploying RELAY SERVER (nats + relay-api + caddy)..."
    cd "$SERVER_DIR"
    docker compose up --build -d

    log_info "Waiting for server to be healthy..."
    sleep 15

    if curl -s http://192.168.1.218:7770/health | grep -q "ok"; then
        log_info "✅ Server is healthy"
    else
        log_warn "Server health check may not be ready yet"
    fi
}

deploy_minions() {
    log_info "Deploying RELAY MINIONS (relay-agent-01/02/03)..."
    cd "$MINION_DIR"
    docker compose up --build -d

    log_info "Waiting for minions to start..."
    sleep 10

    log_info "Checking agent status..."
    for i in 01 02 03; do
        status=$(docker logs relay-agent-$i 2>&1 | grep -o "WebSocket connecté" | tail -1)
        if [ -n "$status" ]; then
            log_info "✅ Agent $i connected"
        else
            log_warn "⚠️  Agent $i status unknown - check logs with: docker logs relay-agent-$i"
        fi
    done
}

stop_all() {
    log_info "Stopping all containers..."

    log_info "Stopping minions..."
    cd "$MINION_DIR"
    docker compose down 2>/dev/null || true

    log_info "Stopping server..."
    cd "$SERVER_DIR"
    docker compose down 2>/dev/null || true

    log_info "✅ All containers stopped"
}

show_status() {
    log_info "RELAY SERVER status:"
    cd "$SERVER_DIR"
    docker compose ps

    echo ""
    log_info "RELAY MINIONS status:"
    cd "$MINION_DIR"
    docker compose ps
}

show_help() {
    cat << EOF
AnsibleRelay Deployment Script

Usage:
    ./deploy.sh [COMMAND] [OPTIONS]

Commands:
    server       Deploy relay server only (nats + relay-api + caddy)
    minion       Deploy relay minions only (relay-agent-01/02/03)
    all          Deploy both server and minions (default)
    stop         Stop all containers
    status       Show status of all containers
    logs-server  Show server logs
    logs-agent   Show agent logs (arg: 01|02|03)
    help         Show this help message

Options:
    DOCKER_HOST  Override Docker host (default: tcp://192.168.1.218:2375)

Examples:
    ./deploy.sh all
    DOCKER_HOST=unix:///var/run/docker.sock ./deploy.sh status
    ./deploy.sh logs-agent 01

EOF
}

# Main
case "${1:-all}" in
    server)
        deploy_server
        ;;
    minion)
        deploy_minions
        ;;
    all)
        deploy_server
        log_info ""
        deploy_minions
        ;;
    stop)
        stop_all
        ;;
    status)
        show_status
        ;;
    logs-server)
        docker logs relay-api --tail 50 -f
        ;;
    logs-agent)
        agent_id="${2:-01}"
        docker logs relay-agent-$agent_id --tail 50 -f
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        log_error "Unknown command: $1"
        show_help
        exit 1
        ;;
esac
