#!/bin/bash
# VRSky Traefik Dashboard Proxy
# Safely exposes the Traefik dashboard via port-forwarding

PORT=9002
NAMESPACE="kube-system"
DEPLOYMENT="traefik"

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}=====================================${NC}"
echo -e "${BLUE}   VRSky Traefik Dashboard Proxy     ${NC}"
echo -e "${BLUE}=====================================${NC}"

# Check if port is already in use
if lsof -Pi :$PORT -sTCP:LISTEN -t >/dev/null; then
	echo -e "Port $PORT is already in use. Attempting to forward anyway..."
fi

echo -e "${GREEN}Forwarding Traefik Dashboard to: http://localhost:$PORT/dashboard/${NC}"
echo -e "${GREEN}Forwarding Traefik Metrics to:   http://localhost:9101/metrics${NC}"
echo -e "Press Ctrl+C to stop forwarding."
echo ""

kubectl port-forward -n $NAMESPACE deploy/$DEPLOYMENT $PORT:8080 9101:9100
