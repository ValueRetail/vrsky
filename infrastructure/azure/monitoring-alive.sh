#!/usr/bin/env bash
# monitoring-alive.sh — is the monitoring itself alive? (plans/monitoring-heartbeat.md)
#
# Every alert goes Prometheus → Alertmanager → management-api → Teams, so a
# dead Prometheus or Alertmanager looks exactly like "all is well". The
# chart's Watchdog alert fires forever for this purpose and is routed to
# nowhere; this script reads it at both ends: Prometheus must report it
# firing (the rules loaded, evaluation runs) and Alertmanager must hold it
# (delivery works). Exit 0 and "monitoring alive", or the missing side on
# stderr and exit 1. Runs in-cluster through in-cluster.sh (bash, jq,
# kubectl) from the Production check workflow; KUBECTL lets a test stand a
# stub in for the real one.
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
P=/api/v1/namespaces/vrsky-monitoring/services/prometheus-prometheus:9090/proxy/api/v1
A=/api/v1/namespaces/vrsky-monitoring/services/prometheus-alertmanager:9093/proxy/api/v2
fail=0

# ALERTS{alertname="Watchdog",alertstate="firing"}
prom=$("$KUBECTL" get --raw "$P/query?query=ALERTS%7Balertname%3D%22Watchdog%22%2Calertstate%3D%22firing%22%7D" 2>/dev/null \
  | jq -r '.data.result | length' 2>/dev/null || echo 0)
if [ "${prom:-0}" -ge 1 ] 2>/dev/null; then
  echo "prometheus: Watchdog firing"
else
  echo "prometheus: no firing Watchdog (unreachable, or the rules did not load)" >&2; fail=1
fi

# alertname="Watchdog"
am=$("$KUBECTL" get --raw "$A/alerts?filter=alertname%3D%22Watchdog%22" 2>/dev/null \
  | jq -r 'length' 2>/dev/null || echo 0)
if [ "${am:-0}" -ge 1 ] 2>/dev/null; then
  echo "alertmanager: holds the Watchdog"
else
  echo "alertmanager: no Watchdog (unreachable, or Prometheus is not delivering alerts)" >&2; fail=1
fi

if [ "$fail" = 0 ]; then echo "monitoring alive"; else echo "monitoring: DEAD" >&2; exit 1; fi
