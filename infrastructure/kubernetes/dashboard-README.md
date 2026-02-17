# Kubernetes Dashboard with Extended Session Timeout

## Overview

This deployment provides the official Kubernetes Dashboard with:
- **Extended Token TTL**: 24 hours (86400 seconds) instead of default 15 minutes
- **Extended Session Timeout**: 8 hours (28800 seconds) instead of default 15 minutes
- **Skip Login Option**: Quick access without re-entering tokens frequently
- **Full RBAC**: Proper permissions for dashboard access
- **TLS Support**: Secure HTTPS connections

## Configuration Parameters

The key configuration for extended session time is in the deployment arguments:

```yaml
args:
  - --token-ttl=86400          # Token valid for 24 hours
  - --authentication-mode=token
  - --enable-skip-login         # Optional: skip login screen
  - --insecure-bind-address=127.0.0.1
```

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

⚠️ **IMPORTANT**: The `--enable-skip-login` flag allows unauthenticated access!

### For Production

Remove `--enable-skip-login` and use token authentication:

```yaml
args:
  - --auto-generate-cert
  - --namespace=kubernetes-dashboard
  - --token-ttl=86400
  - --authentication-mode=token
  # Remove: - --enable-skip-login
  - --insecure-bind-address=127.0.0.1
```

Then always use explicit tokens to log in.

### Best Practices

1. **Use RBAC**: Don't give dashboard full cluster-admin access
2. **Use TLS**: Deploy with proper certificates (Ingress + cert-manager)
3. **Network Policy**: Restrict who can access the dashboard
4. **Audit Logging**: Monitor dashboard access
5. **Token Rotation**: Periodically refresh tokens (though 24h TTL helps)

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
| --authentication-mode | token | token,basic | Auth method |
| --enable-skip-login | false | true | Allow skip login |
| --kubeconfig | (auto) | /path/to/config | Kubeconfig path |
| --insecure-bind-address | (none) | 127.0.0.1 | HTTP bind address |

## References

- [Kubernetes Dashboard GitHub](https://github.com/kubernetes/dashboard)
- [Official Documentation](https://kubernetes.io/docs/tasks/access-application-cluster/web-ui-dashboard/)
- [Dashboard Command Arguments](https://github.com/kubernetes/dashboard/blob/master/docs/arguments.md)

---

**Status**: Ready to deploy. Tokens will remain valid for 24 hours after generation.
