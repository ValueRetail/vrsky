# Inbound webhook mTLS

A webhook connection with `tls.client_ca_secret_id` only accepts callers that
present a client certificate signed by that CA
([HTTP & webhooks](../connectors/http.md#mutual-tls-client-certificates)). On
AKS the certificate is checked by ingress-nginx on its own host,
`mtls.vrsky.valueretail.no`, and again by the webhook consumer against the
connection's own CA (#454).

```
caller --TLS + client cert--> ingress-nginx, host mtls.vrsky.valueretail.no
         checks it against the Secret webhook-client-ca (every tenant's CA)
         writes it into ssl-client-cert, replacing any copy the caller sent
       --TLS, nginx presents webhook-edge-client--> webhook consumer :9102
         accepts only that certificate (private edge CA)
         checks ssl-client-cert against THIS connection's CA
```

The files, all in `infrastructure/kubernetes/ingress/` and all applied by
hand (the deploy job only rolls the connector manifest):

| File | What |
| --- | --- |
| `webhook-edge-tls.yaml` | cert-manager: a private CA, the worker's server certificate (`webhook-edge-server`), nginx's client certificate (`webhook-edge-client`) |
| `webhook-client-ca.example.yaml` | the shape of the CA bundle Secret; create the real one with `kubectl` |
| `webhooks-mtls-ingress.yaml` | the Ingress with the `auth-tls-*` and `proxy-ssl-*` annotations |
| `webhook-consumer-networkpolicy.yaml` | optional: only ingress-nginx may reach 9102 |

The webhook consumer side (env, port 9102, the optional Secret mount) is in
the connector manifest and deploys on merge. Until the edge Secret exists the
consumer logs `Edge mTLS listener not started` once at start, and everything
else works as before.

## Rollout

Run from a shell that may reach the cluster. Read-only checks first.

**0. Check what is there.**

```bash
kubectl get pods -A -l app.kubernetes.io/name=ingress-nginx --show-labels
kubectl -n ingress-nginx get deploy -o jsonpath='{..image}{"\n"}'
kubectl get clusterissuer letsencrypt-prod
az aks show -g vrsky-prod -n vrsky-prod --query networkProfile.networkPolicy -o tsv
```

The NetworkPolicy file assumes namespace `ingress-nginx` and the label
`app.kubernetes.io/name=ingress-nginx`; fix it if the first command says
otherwise. If the last command prints nothing or `none`, the cluster enforces
no NetworkPolicy and step 7 changes nothing (the mTLS hop to the worker is the
boundary either way).

**1. DNS.** Point `mtls.vrsky.valueretail.no` at the same address as
`vrsky.valueretail.no` (the ingress LoadBalancer):

```bash
dig +short vrsky.valueretail.no
dig +short mtls.vrsky.valueretail.no   # must print the same address
```

**2. The edge certificates.**

```bash
kubectl apply -f infrastructure/kubernetes/ingress/webhook-edge-tls.yaml
kubectl -n vrsky-platform wait --for=condition=Ready \
  certificate/webhook-edge-ca certificate/webhook-edge-server certificate/webhook-edge-client --timeout=120s
```

**3. Restart the webhook consumer** so it reads the new Secret:

```bash
kubectl -n vrsky-platform rollout restart deploy/vrsky-webhook-consumer
kubectl -n vrsky-platform rollout status deploy/vrsky-webhook-consumer
kubectl -n vrsky-platform logs deploy/vrsky-webhook-consumer | grep -i 'edge mtls'
# want: "Edge mTLS webhook listener started" addr=[::]:9102
```

**4. The Ingress, before the CA bundle exists.** This also checks that a
missing bundle fails closed:

```bash
kubectl apply -f infrastructure/kubernetes/ingress/webhooks-mtls-ingress.yaml
kubectl -n vrsky-platform wait --for=condition=Ready certificate/vrsky-webhooks-mtls-tls --timeout=300s
U=https://mtls.vrsky.valueretail.no/webhook/x
curl -s -o /dev/null -w '%{http_code}\n' "$U" -d '{}'
# want 503 (ingress-nginx denies the location while auth-tls-secret is missing)
curl -s -o /dev/null -w '%{http_code}\n' "$U" -d '{}' \
  -H 'ssl-client-cert: forged' -H 'ssl-client-verify: SUCCESS'
# want 503 too: a forged header must not get through a broken edge
```

Then the same with a bundle that doesn't parse:

```bash
printf 'not a certificate\n' > junk.pem
kubectl -n vrsky-platform create secret generic webhook-client-ca --from-file=ca.crt=junk.pem
sleep 10
curl -s -o /dev/null -w '%{http_code}\n' "$U" -d '{}' \
  -H 'ssl-client-cert: forged' -H 'ssl-client-verify: SUCCESS'
# want 503 (or a TLS error); never 401 or 202
kubectl -n vrsky-platform delete secret webhook-client-ca
```

If any of these prints anything else, delete the Ingress (rollback below) and
stop: the worker would still verify a forged certificate against the
connection's CA, but nothing would prove the caller holds its key.

**5. The CA bundle.** One PEM file with every CA that a tenant has set as a
webhook client CA. The tenant supplies their CA certificate (public material;
VRSky stores it encrypted, and the bundle copy is only for the edge). For a
first test, use a throwaway CA from the checks below.

```bash
cat tenant-a-ca.pem tenant-b-ca.pem > bundle.pem
kubectl -n vrsky-platform create secret generic webhook-client-ca \
  --from-file=ca.crt=bundle.pem --dry-run=client -o yaml | kubectl apply -f -
```

ingress-nginx picks the Secret up by itself within a few seconds. Repeat this
step whenever a tenant adds or rotates a client CA.

Never add a public CA, one that issues certificates to anyone who asks. If a
tenant's client certificates come from an intermediate, put the intermediate
and its root in the bundle, and tell the tenant their connection's client CA
must include the intermediate: the ingress forwards only the leaf, so the
webhook service can't build the chain from the root alone.

**6. Checks.** Make two throwaway CAs and a client certificate from each, on
your machine:

```bash
for n in a b; do
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
    -subj "/CN=test-ca-$n" -keyout ca-$n.key -out ca-$n.crt \
    -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=client-$n" \
    -keyout client-$n.key -out client-$n.csr
  printf 'extendedKeyUsage=clientAuth\n' > ext
  openssl x509 -req -in client-$n.csr -CA ca-$n.crt -CAkey ca-$n.key -CAcreateserial \
    -days 1 -extfile ext -out client-$n.crt
done
cat ca-a.crt ca-b.crt > bundle.pem   # step 5 with this bundle (plus the real CAs, if any)
```

Make a test pipeline with a Webhook (HTTP) source, store `ca-a.crt` as a
tenant secret, and set the source node's `tls.client_ca_secret_id` to it
through the connections API (the builder has no field for it yet; see
[Config surface](../SECURITY.md#config-surface)). Start it and note its
connection id. Then:

```bash
U=https://mtls.vrsky.valueretail.no/webhook/<connection-id>
A=$(python3 -c 'import urllib.parse;print(urllib.parse.quote(open("client-a.crt").read(),safe=""))')
c() { curl -s -o /dev/null -w '%{http_code}\n' -d '{"ping":true}' "$@"; }

c "$U"                                                    # 400: no certificate
c --cert client-a.crt --key client-a.key "$U"             # 202
c --cert client-b.crt --key client-b.key "$U"             # 401: CA b is in the bundle, not this connection's
c --cert client-b.crt --key client-b.key \
  -H "ssl-client-cert: $A" -H 'ssl-client-verify: SUCCESS' "$U"   # 401: nginx replaced the forged header
c -H "ssl-client-cert: $A" -H 'ssl-client-verify: SUCCESS' \
  https://vrsky.valueretail.no/webhook/<connection-id>   # 401: the public host never trusts the header
```

From inside the cluster, the edge port refuses anything but the ingress (any
image with curl the cluster can pull will do):

```bash
kubectl -n vrsky-platform run edge-probe --rm -i --restart=Never --image=curlimages/curl -- \
  -sk -o /dev/null -w '%{http_code}\n' https://vrsky-webhook-consumer:9102/webhook/x -d '{}'
# want 000 and a TLS error: the handshake needs nginx's client certificate
```

The consumer logs each refusal with the subject and SHA-256 fingerprint of
the certificate, and `webhook_client_cert_failures_total` counts them by
reason. Delete the test pipeline and take `ca-a`/`ca-b` out of the bundle
(step 5) afterwards.

**7. Optional: the NetworkPolicy**, if step 0 showed an enforcing engine:

```bash
kubectl apply -f infrastructure/kubernetes/ingress/webhook-consumer-networkpolicy.yaml
```

Then repeat the `202` check and an ordinary webhook to
`https://vrsky.valueretail.no/webhook/<id>`, and check the management API can
still reach the consumer (the webhook source panel's sample data loads).

## Rollback

```bash
kubectl -n vrsky-platform delete ingress vrsky-webhooks-mtls
kubectl -n vrsky-platform delete networkpolicy vrsky-webhook-consumer-edge --ignore-not-found
```

That closes the mTLS host; mTLS connections refuse every request again, as
before #454, and nothing else changes. The edge port only accepts nginx's
certificate, so it can stay. To remove everything, also
`kubectl delete -f infrastructure/kubernetes/ingress/webhook-edge-tls.yaml`,
delete the Secrets `webhook-edge-ca`, `webhook-edge-server`,
`webhook-edge-client`, `webhook-client-ca` and `vrsky-webhooks-mtls-tls`, and
revert the PR (the consumer then loses port 9102 on the next deploy).

## Renewal

cert-manager renews the edge certificates 90 days before they end. nginx
reloads a changed Secret by itself; the webhook consumer reads its files at
start, and pods restart at every cluster start
([cost parking](cost-parking.md)), well inside those 90 days. The edge CA
keeps its key across renewals.
