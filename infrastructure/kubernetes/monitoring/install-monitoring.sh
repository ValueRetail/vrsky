#!/bin/bash
# install-monitoring.sh — Prometheus (kube-prometheus-stack) + Grafana via Helm.
#
#   ./install-monitoring.sh                 # k3s lab: base values only
#   PROFILE=azure ./install-monitoring.sh   # prod/AKS: adds the *.azure.yaml
#                                           # overlays, PodMonitors, the VRSky
#                                           # PrometheusRule and expects the
#                                           # alerts-webhook-token + grafana-admin
#                                           # Secrets to exist (see README)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RULES_FILE="$SCRIPT_DIR/../../prometheus-rules.yml"
PROFILE="${PROFILE:-}"
NS=vrsky-monitoring

if ! command -v helm &>/dev/null; then
	echo "Error: Helm is not installed. Install with:"
	echo "  curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash"
	exit 1
fi

prom_values=(--values "$SCRIPT_DIR/prometheus-values.yaml")
graf_values=(--values "$SCRIPT_DIR/grafana-values.yaml")
if [[ "$PROFILE" == "azure" ]]; then
	prom_values+=(--values "$SCRIPT_DIR/prometheus-values.azure.yaml")
	graf_values+=(--values "$SCRIPT_DIR/grafana-values.azure.yaml")
	# Fail before Helm does, with a message that says what to create.
	for s in alerts-webhook-token grafana-admin; do
		if ! kubectl -n "$NS" get secret "$s" &>/dev/null; then
			echo "Error: Secret $NS/$s is missing. Create it first (README → Azure profile)."
			exit 1
		fi
	done
fi

echo "Installing VRSky monitoring stack (profile: ${PROFILE:-base})"
kubectl apply -f "$SCRIPT_DIR/namespace.yaml"

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null
helm repo add grafana https://grafana.github.io/helm-charts >/dev/null
helm repo update >/dev/null

echo "Installing Prometheus..."
helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
	--namespace "$NS" "${prom_values[@]}" --wait --timeout 10m

if [[ "$PROFILE" == "azure" ]]; then
	echo "Applying VRSky scrape targets..."
	kubectl apply -f "$SCRIPT_DIR/podmonitors.yaml"

	# The alert rules live in ONE file (infrastructure/prometheus-rules.yml,
	# unit-tested with promtool in CI). Wrap it in a PrometheusRule here rather
	# than keeping a second copy that would drift.
	echo "Applying VRSky alert rules..."
	{
		echo "apiVersion: monitoring.coreos.com/v1"
		echo "kind: PrometheusRule"
		echo "metadata:"
		echo "  name: vrsky-alerts"
		echo "  namespace: $NS"
		echo "spec:"
		sed 's/^/  /' "$RULES_FILE"
	} | kubectl apply -f -
fi

echo "Installing Grafana..."
helm upgrade --install grafana grafana/grafana \
	--namespace "$NS" "${graf_values[@]}" --wait --timeout 5m

echo ""
echo "Monitoring stack installed."
echo "  Grafana:      kubectl port-forward -n $NS svc/grafana 13000:80            → http://localhost:13000"
echo "  Prometheus:   kubectl port-forward -n $NS svc/prometheus-prometheus 19090:9090 → http://localhost:19090/targets"
echo "  Alertmanager: kubectl port-forward -n $NS svc/prometheus-alertmanager 19093:9093"
if [[ "$PROFILE" == "azure" ]]; then
	echo "  Grafana admin password: the grafana-admin Secret."
else
	echo "  Grafana admin: see grafana-values.yaml (change it before any shared use)."
fi
