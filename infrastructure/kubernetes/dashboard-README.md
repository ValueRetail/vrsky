# Kubernetes Dashboard with Extended Session Timeout

## Overview

This deployment provides the official Kubernetes Dashboard with:
- **Extended Token TTL**: 24 hours (86400 seconds) instead of default 15 minutes
- **Extended Session Timeout**: 8 hours (28800 seconds) instead of default 15 minutes
- **Full RBAC**: Proper permissions for dashboard access
- **TLS Support**: Secure HTTPS connections
- **Security Hardened**: Skip-login disabled by default to prevent unauthenticated access

## Configuration Parameters

The deployment is configured for secure production use:

```yaml
args:
  - --auto-generate-cert
  - --namespace=kubernetes-dashboard
  - --authentication-mode=token
  # Skip-login is disabled for security (remove comment to enable for non-prod only)
  # - --enable-skip-login
  - --insecure-bind-address=127.0.0.1
```

**Security Note**: The `--enable-skip-login` flag is intentionally disabled. Enabling it allows unauthenticated access to the dashboard and is only suitable for non-production testing environments.

## Deployment

### Quick Deploy

```bash
# Deploy the dashboard
kubectl apply -f kubernetes-dashboard.yaml

# Verify deployment
kubectl get deployment -n kubernetes-dashboard
kubectl get pods -n kubernetes-dashboard

# Wait for pod to be ready (30-60 seconds)
kubectl wait --for=condition=available --timeout=300s \
  deployment/kubernetes-dashboard -n kubernetes-dashboard
```

### Access the Dashboard

**Option 1: Port Forward (Local Development)**

```bash
# Forward port 8443 to localhost
kubectl port-forward -n kubernetes-dashboard \
  svc/kubernetes-dashboard 8443:443

# Access in browser: https://localhost:8443
# Bypass security warning (self-signed cert) and click "Skip" or get token
```

**Option 2: Create Service Account Token**

```bash
# Create admin user for dashboard access
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: admin-user
  namespace: kubernetes-dashboard

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: admin-user
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: admin-user
  namespace: kubernetes-dashboard
EOF

# Get the token (valid for 24 hours now!)
kubectl -n kubernetes-dashboard create token admin-user

# Use this token to login - it will stay valid for 24 hours
```

**Option 3: Ingress (Production)**

```bash
# Create ingress rule
kubectl apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kubernetes-dashboard
  namespace: kubernetes-dashboard
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
    nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
spec:
  ingressClassName: nginx
  tls:
  - hosts:
    - dashboard.vrsky.example.com
    secretName: kubernetes-dashboard-tls
  rules:
  - host: dashboard.vrsky.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: kubernetes-dashboard
            port:
              number: 443
EOF

# Access at: https://dashboard.vrsky.example.com
```

## Session Timeout Behavior

### Before Configuration
- Token TTL: 15 minutes (900 seconds)
- Session: Expires on browser close or idle
- User Experience: Frequent re-authentication required

### After Configuration
- Token TTL: 24 hours (86400 seconds) ✅
- Session: Remains valid for 8 hours of inactivity
- User Experience: Single login per day, minimal interruptions

## Token Management

### Get Current Token

```bash
# Token remains valid for 24 hours
TOKEN=$(kubectl -n kubernetes-dashboard create token admin-user)
echo $TOKEN

# Use token to access API
curl -k -H "Authorization: Bearer $TOKEN" \
  https://localhost:8443/api/v1/nodes
```

### Token Expiration Check

```bash
# Check when token will expire (24 hours from creation)
# Tokens are created on-demand, so they expire 24 hours after creation

# Get a fresh token that's valid for another 24 hours
kubectl -n kubernetes-dashboard create token admin-user --duration=24h
```

## Security Considerations

🔒 **SECURITY BY DEFAULT**: This deployment disables `--enable-skip-login` to prevent unauthenticated access.

### Critical Security Notes

- ⚠️ **NEVER enable `--enable-skip-login` in production** - it allows full cluster access without authentication
- ⚠️ **Token-based authentication only** - always require valid tokens to access the dashboard
- ⚠️ **RBAC-restricted service account** - this deployment uses cluster-admin (for testing only)

### For Production Deployments

1. **Remove cluster-admin access**: Create a limited service account with only necessary permissions
2. **Use TLS with Ingress**: Deploy behind nginx-ingress with cert-manager for HTTPS
3. **Network Policy**: Restrict access to trusted networks only
4. **Audit Logging**: Enable and monitor all dashboard access
5. **Token Rotation**: Even with 24-hour TTL, rotate tokens regularly
6. **No Unauthenticated Access**: Keep `--enable-skip-login` disabled (it is disabled by default)

### Testing Only: Enable Skip-Login

To enable skip-login ONLY for non-production testing clusters, edit `kubernetes-dashboard.yaml`:

```yaml
args:
  - --auto-generate-cert
  - --namespace=kubernetes-dashboard
  - --authentication-mode=token
  - --enable-skip-login         # ⚠️ TESTING ONLY - DO NOT USE IN PRODUCTION
  - --insecure-bind-address=127.0.0.1
```

Then use `./dashboard.sh` to access it.

## Uninstall

```bash
# Remove the dashboard
kubectl delete -f kubernetes-dashboard.yaml

# Remove admin user (if created)
kubectl delete serviceaccount admin-user -n kubernetes-dashboard
kubectl delete clusterrolebinding admin-user
```

## Troubleshooting

### Dashboard Pod Won't Start

```bash
# Check pod logs
kubectl logs -n kubernetes-dashboard deployment/kubernetes-dashboard

# Check pod events
kubectl describe pod -n kubernetes-dashboard \
  -l k8s-app=kubernetes-dashboard
```

### Token Not Working

```bash
# Generate new token
kubectl -n kubernetes-dashboard create token admin-user

# Verify RBAC permissions
kubectl auth can-i get nodes \
  --as=system:serviceaccount:kubernetes-dashboard:admin-user
```

### Can't Access via Ingress

```bash
# Check ingress status
kubectl get ingress -n kubernetes-dashboard

# Check certificate
kubectl get certificate -n kubernetes-dashboard

# Check ingress controller
kubectl get pods -n ingress-nginx
```

## Configuration Options

You can customize the dashboard further:

| Flag | Default | Example | Notes |
|------|---------|---------|-------|
| `--token-ttl` | 15m | 86400s | Token validity duration |
| `--authentication-mode` | token | token,basic | Auth method |
| `--enable-skip-login` | false | true | Allow skip login |
| `--kubeconfig` | (auto) | /path/to/config | Kubeconfig path |
| `--insecure-bind-address` | (none) | 127.0.0.1 | HTTP bind address |

## References

- [Kubernetes Dashboard GitHub](https://github.com/kubernetes/dashboard)
- [Official Documentation](https://kubernetes.io/docs/tasks/access-application-cluster/web-ui-dashboard/)
- [Dashboard Command Arguments](https://github.com/kubernetes/dashboard/blob/master/docs/arguments.md)

---

**Status**: Ready to deploy. Tokens will remain valid for 24 hours after generation.
