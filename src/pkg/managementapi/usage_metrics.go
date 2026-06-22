package managementapi

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// connectionDeploys counts successful connection starts (deploys) per tenant
// (Phase 4A / #92). It is scraped from the management-api /metrics endpoint; the
// usage rollup reads increase() of it from Prometheus into usage_daily.deploys.
var connectionDeploys = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "vrsky_connection_deploys_total",
	Help: "Successful connection starts (deploys) by tenant.",
}, []string{"tenant_id"})
