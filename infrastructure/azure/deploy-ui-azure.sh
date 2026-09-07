#!/usr/bin/env bash
# deploy-ui-azure.sh — deploy the VRSky UI on AKS.
#
# The SPA calls the API same-origin (axios baseURL:'' + a cookie rule), but the
# stock UI nginx doesn't proxy /api. So we mount a custom nginx config that
# serves the SPA AND proxies /api + /ws to the in-cluster management-api.
#
# This script used to describe a PRIVATE UI reached over `kubectl port-forward`,
# with "no public ingress, no extra controller". That stopped being true: the
# cluster now runs ingress-nginx and serves the UI publicly over TLS via the
# `vrsky` Ingress (infrastructure/kubernetes/ui/ingress.yaml, applied below).
# Port-forward still works and is still the way in when the ingress is down.
set -euo pipefail

REG="${REG:-vrskyprodacr}"
ACR_LOGIN="${REG}.azurecr.io"
MGMT="vrsky-management-api.vrsky-platform.svc.cluster.local:8080"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# 1. namespace + reuse the acr-pull secret (copied from vrsky-platform, no creds in this script)
kubectl create namespace vrsky-ui --dry-run=client -o yaml | kubectl apply -f -
kubectl get secret acr-pull -n vrsky-platform -o yaml \
  | sed '/namespace:/d;/resourceVersion:/d;/uid:/d;/creationTimestamp:/d;/^\s*selfLink:/d' \
  | kubectl apply -n vrsky-ui -f -
kubectl patch serviceaccount default -n vrsky-ui -p '{"imagePullSecrets":[{"name":"acr-pull"}]}'

# 2. nginx config: SPA + same-origin proxy to the management-api
kubectl create configmap vrsky-ui-nginx -n vrsky-ui --dry-run=client -o yaml \
  --from-literal=default.conf="server {
    listen 80 default_server;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    location /api/ {
        proxy_pass http://${MGMT};
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
    location /ws {
        proxy_pass http://${MGMT};
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \"upgrade\";
        proxy_set_header Host \$host;
    }
    location /assets/ { expires 1y; add_header Cache-Control \"public, immutable\"; try_files \$uri =404; }
    location /health { access_log off; return 200 \"healthy\n\"; add_header Content-Type text/plain; }
    location / {
        try_files \$uri \$uri/ /index.html;
        add_header Cache-Control \"no-cache, no-store, must-revalidate\";
    }
}" | kubectl apply -f -

# 3. UI config + service (from the repo, unchanged)
kubectl apply -f infrastructure/kubernetes/ui/configmap.yaml
kubectl apply -f infrastructure/kubernetes/ui/service.yaml

# 4. UI deployment: ACR image, 1 replica (small nodes), mount the nginx config
WORKUI="$(mktemp -d)/ui-deployment.yaml"
cp infrastructure/kubernetes/ui/deployment.yaml "$WORKUI"
perl -pi -e 's{ghcr\.io/[Vv]alue[Rr]etail/vrsky/ui:latest}{'"$ACR_LOGIN"'/vrsky/ui:latest}g;' "$WORKUI"
# The manifest carries imagePullPolicy: IfNotPresent, which is correct for the
# k3d path it is shared with (images are side-loaded, there is no registry to
# pull from — see infrastructure/scripts/k3d-load-images.sh). On AKS with a
# MUTABLE :latest tag it is a trap: a node that already has *a* :latest keeps
# serving it, so `kubectl rollout restart` reports a successful rollout and
# redeploys the old build. That is exactly what happened on 2026-09-07 — a
# freshly pushed UI image, a green rollout, and the previous bundle still live.
perl -pi -e 's/^(\s*imagePullPolicy:)\s*IfNotPresent\s*$/${1} Always/;' "$WORKUI"
perl -pi -e 's/^(\s*replicas:)\s*3\b/${1} 2/;' "$WORKUI"
# mount the nginx config over the image's default.conf
perl -0777 -pi -e 's/(        - name: tmp\n          mountPath: \/tmp\n)/$1        - name: nginx-conf\n          mountPath: \/etc\/nginx\/conf.d\/default.conf\n          subPath: default.conf\n/' "$WORKUI"
perl -0777 -pi -e 's/(      - name: tmp\n        emptyDir: \{\}\n)/$1      - name: nginx-conf\n        configMap:\n          name: vrsky-ui-nginx\n/' "$WORKUI"
kubectl apply -f "$WORKUI"

# HA: PodDisruptionBudget so a node drain keeps a UI replica up (pairs with
# replicas: 2 above).
kubectl apply -f infrastructure/kubernetes/ui/pdb.yaml

# Public ingress + TLS. This owns the certificate for the host; the
# vrsky-webhooks Ingress depends on it (see that file's header).
kubectl apply -f infrastructure/kubernetes/ui/ingress.yaml

echo ">>> waiting for UI to roll out..."
kubectl rollout status deploy/vrsky-ui -n vrsky-ui --timeout=180s

cat <<EOF

UI deployed. Public:  https://20.251.107.2.sslip.io/

Or privately, without the ingress:
  kubectl port-forward -n vrsky-ui svc/vrsky-ui 8080:80
Then browse http://localhost:8080  (the UI proxies /api + /ws to the management-api).

Verify the build that is actually being served — a rollout can succeed while
serving a stale bundle if the image reference did not change:
  kubectl -n vrsky-ui exec deploy/vrsky-ui -- ls /usr/share/nginx/html/assets
EOF
