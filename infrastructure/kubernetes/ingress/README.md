# VRSky Ingress & TLS Configuration

This directory contains Kubernetes Ingress rules and cert-manager configuration for exposing VRSky services with automatic TLS certificates.

## Architecture

### Components

- **Ingress Controller**: Nginx (installed by K3s by default)
- **TLS Manager**: cert-manager with Let's Encrypt
- **Certificate Issuers**: Staging and Production

### Exposed Services

| Domain                      | Service           | Port | Purpose                                             |
| --------------------------- | ----------------- | ---- | --------------------------------------------------- |
| `api.vrsky.example.com`     | vrsky-api-gateway | 8080 | Control Plane API (tenant management, integrations) |
| `grafana.vrsky.example.com` | grafana           | 80   | Monitoring dashboards                               |

**Note**: Tenant-specific integrations (webhooks, etc.) will be added dynamically.

## Installation

### Prerequisites

```bash
# Verify Nginx Ingress Controller is running (K3s installs it by default)
kubectl get pods -n kube-system | grep ingress

# Expected output:
# svclb-traefik-xxxxx  (K3s uses Traefik by default)
# OR
# ingress-nginx-controller-xxxxx
```

If using K3s with Traefik (default), you can either:

1. Use Traefik (change ingress class to `traefik`)
2. Disable Traefik and install Nginx:

```bash
# Disable Traefik in K3s
# Add to K3s install: --disable traefik

# Install Nginx Ingress
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx \
  --create-namespace \
  --set controller.service.type=LoadBalancer
```

### Quick Install

```bash
# 1. Install cert-manager
./install-cert-manager.sh

# 2. Update domains in cert-issuers.yaml and ingress.yaml
# CHANGE:
#   - admin@vrsky.example.com → your email
#   - api.vrsky.example.com → your domain
#   - grafana.vrsky.example.com → your domain

# 3. Create ClusterIssuers
kubectl apply -f cert-issuers.yaml

# 4. Create Ingress rules (after services are deployed)
kubectl apply -f ingress.yaml
```

### Manual Install

```bash
# 1. Install cert-manager
helm repo add jetstack https://charts.jetstack.io
helm repo update
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --version v1.13.3 \
  --set installCRDs=true

# 2. Wait for cert-manager to be ready
kubectl wait --for=condition=ready pod \
  -l app.kubernetes.io/instance=cert-manager \
  -n cert-manager \
  --timeout=120s

# 3. Edit and apply ClusterIssuers
# Update email in cert-issuers.yaml
kubectl apply -f cert-issuers.yaml

# 4. Edit and apply Ingress
# Update domains in ingress.yaml
kubectl apply -f ingress.yaml
```

## Configuration

### Update Domains

Edit `ingress.yaml` and replace:

```yaml
# BEFORE (example domains)
- host: api.vrsky.example.com
- host: grafana.vrsky.example.com

# AFTER (your actual domains)
- host: api.vrsky.yourdomain.com
- host: grafana.vrsky.yourdomain.com
```

### Update Email for Let's Encrypt

Edit `cert-issuers.yaml`:

```yaml
# BEFORE
email: admin@vrsky.example.com

# AFTER
email: your-email@yourdomain.com
```

### Switch from Staging to Production

After testing with staging certificates, switch to production:

Edit `ingress.yaml`:

```yaml
# BEFORE
cert-manager.io/cluster-issuer: letsencrypt-staging

# AFTER
cert-manager.io/cluster-issuer: letsencrypt-prod
```

Apply:

```bash
kubectl apply -f ingress.yaml
```

## DNS Configuration

### Point Domains to Cluster

Get your cluster's external IP:

```bash
# For K3s with LoadBalancer (cloud)
kubectl get svc -n kube-system traefik

# For K3s on VPS (NodePort)
# Use your VPS public IP
echo "Your VPS IP: <MASTER_IP>"
```

Add DNS A records:

```
api.vrsky.yourdomain.com     A   <CLUSTER_IP>
grafana.vrsky.yourdomain.com A   <CLUSTER_IP>
*.vrsky.yourdomain.com       A   <CLUSTER_IP>  (wildcard for tenant webhooks)
```

### Using Cloudflare DNS

If using Cloudflare (recommended):

1. Log in to Cloudflare dashboard
2. Select your domain
3. Go to DNS → Records
4. Add A records:

| Type | Name          | Content        | Proxy Status | TTL  |
| ---- | ------------- | -------------- | ------------ | ---- |
| A    | api.vrsky     | `<CLUSTER_IP>` | Proxied      | Auto |
| A    | grafana.vrsky | `<CLUSTER_IP>` | Proxied      | Auto |
| A    | \*.vrsky      | `<CLUSTER_IP>` | DNS only     | Auto |

**Note**: For wildcard (`*.vrsky`), disable Cloudflare proxy to allow Let's Encrypt HTTP-01 challenge.

## Verification

### Check cert-manager Installation

```bash
# Verify cert-manager pods
kubectl get pods -n cert-manager

# Expected output:
# cert-manager-xxxxxxxxx-xxxxx           1/1     Running
# cert-manager-cainjector-xxxxxx-xxxxx   1/1     Running
# cert-manager-webhook-xxxxxxxxx-xxxxx   1/1     Running
```

### Check ClusterIssuers

```bash
# List ClusterIssuers
kubectl get clusterissuer

# Expected output:
# NAME                  READY   AGE
# letsencrypt-staging   True    5m
# letsencrypt-prod      True    5m

# Check issuer details
kubectl describe clusterissuer letsencrypt-staging
```

### Check Ingress

```bash
# List all ingresses
kubectl get ingress -A

# Check specific ingress
kubectl describe ingress vrsky-ingress -n vrsky-platform
kubectl describe ingress grafana-ingress -n vrsky-monitoring
```

### Check TLS Certificates

```bash
# List certificates
kubectl get certificate -A

# Check certificate details
kubectl describe certificate vrsky-tls-cert -n vrsky-platform

# Check certificate status
kubectl get certificate vrsky-tls-cert -n vrsky-platform -o jsonpath='{.status.conditions[0].message}'
```

### Test HTTPS Access

```bash
# Test API endpoint (will fail until API Gateway is deployed)
curl -I https://api.vrsky.yourdomain.com

# Test Grafana
curl -I https://grafana.vrsky.yourdomain.com

# Expected: HTTP/2 200 or 302 (redirect to login)
```

## Troubleshooting

### Certificate Not Issuing

```bash
# Check certificate status
kubectl describe certificate vrsky-tls-cert -n vrsky-platform

# Check certificate request
kubectl get certificaterequest -n vrsky-platform

# Check certificate order
kubectl get order -n vrsky-platform

# Check challenge
kubectl get challenge -n vrsky-platform

# View cert-manager logs
kubectl logs -n cert-manager -l app=cert-manager -f
```

Common issues:

- **DNS not resolving**: Verify A records point to cluster IP
- **Firewall blocking port 80**: Let's Encrypt needs HTTP-01 challenge on port 80
- **Cloudflare proxy enabled on wildcard**: Disable proxy for `*.vrsky`
- **Email not valid**: Update email in ClusterIssuer

### Ingress Not Working

```bash
# Check Ingress Controller
kubectl get pods -n kube-system | grep ingress

# Check Ingress events
kubectl describe ingress vrsky-ingress -n vrsky-platform

# Check Ingress Controller logs
kubectl logs -n kube-system -l app.kubernetes.io/name=ingress-nginx -f
```

### HTTPS Redirect Loop

```bash
# Check Cloudflare SSL/TLS setting
# Should be: Full (strict) or Full

# If using Cloudflare proxy, add annotation:
nginx.ingress.kubernetes.io/ssl-redirect: "false"
```

### 502 Bad Gateway

```bash
# Service not found or pods not ready
kubectl get svc -n vrsky-platform
kubectl get pods -n vrsky-platform

# Check backend service
kubectl describe svc vrsky-api-gateway -n vrsky-platform
```

## Adding New Ingress Rules

### For Tenant Webhooks

When a new integration with webhook is created:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: webhook-${TENANT_ID}-${INTEGRATION_ID}
  namespace: vrsky-platform
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
    - hosts:
        - webhook-${INTEGRATION_ID}.vrsky.yourdomain.com
      secretName: webhook-${INTEGRATION_ID}-tls
  rules:
    - host: webhook-${INTEGRATION_ID}.vrsky.yourdomain.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: vrsky-webhook-handler
                port:
                  number: 8080
```

### For Admin UI (Future)

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: admin-ui-ingress
  namespace: vrsky-platform
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
    - hosts:
        - app.vrsky.yourdomain.com
      secretName: admin-ui-tls
  rules:
    - host: app.vrsky.yourdomain.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: vrsky-admin-ui
                port:
                  number: 80
```

## Security Best Practices

### Rate Limiting

Add to Ingress annotations:

```yaml
nginx.ingress.kubernetes.io/limit-rps: "100"
nginx.ingress.kubernetes.io/limit-connections: "50"
```

### IP Whitelisting

Restrict access to specific IPs:

```yaml
nginx.ingress.kubernetes.io/whitelist-source-range: "1.2.3.4/32,5.6.7.8/32"
```

### Basic Auth (for staging environments)

```bash
# Create basic auth secret
htpasswd -c auth admin
kubectl create secret generic basic-auth --from-file=auth -n vrsky-platform

# Add annotation to Ingress
nginx.ingress.kubernetes.io/auth-type: basic
nginx.ingress.kubernetes.io/auth-secret: basic-auth
nginx.ingress.kubernetes.io/auth-realm: 'Authentication Required'
```

## Maintenance

### Renew Certificates Manually

Certificates auto-renew 30 days before expiry. To force renewal:

```bash
# Delete certificate (will trigger re-issuance)
kubectl delete certificate vrsky-tls-cert -n vrsky-platform

# Or annotate for renewal
kubectl annotate certificate vrsky-tls-cert -n vrsky-platform \
  cert-manager.io/issue-temporary-certificate="true" --overwrite
```

### Update Ingress Rules

```bash
# Edit ingress
kubectl edit ingress vrsky-ingress -n vrsky-platform

# Or apply updated file
kubectl apply -f ingress.yaml
```

### Monitor Certificate Expiry

```bash
# Check expiry dates
kubectl get certificate -A -o custom-columns=\
NAME:.metadata.name,\
NAMESPACE:.metadata.namespace,\
READY:.status.conditions[0].status,\
EXPIRY:.status.notAfter
```

## Uninstall

```bash
# Delete Ingress rules
kubectl delete -f ingress.yaml

# Delete ClusterIssuers
kubectl delete -f cert-issuers.yaml

# Uninstall cert-manager
helm uninstall cert-manager -n cert-manager
kubectl delete namespace cert-manager

# Delete CRDs (if needed)
kubectl delete crd certificates.cert-manager.io
kubectl delete crd certificaterequests.cert-manager.io
kubectl delete crd challenges.acme.cert-manager.io
kubectl delete crd clusterissuers.cert-manager.io
kubectl delete crd issuers.cert-manager.io
kubectl delete crd orders.acme.cert-manager.io
```

## References

- [cert-manager Documentation](https://cert-manager.io/docs/)
- [Let's Encrypt](https://letsencrypt.org/docs/)
- [Nginx Ingress Controller](https://kubernetes.github.io/ingress-nginx/)
- [Kubernetes Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/)
