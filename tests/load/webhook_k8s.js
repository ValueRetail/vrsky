// k8s orchestrator-path load scenario (#15 scaled-topology run).
//
// Drives a constant arrival rate of JSON POSTs at the pkg/runtime consumer's
// HTTP input (`POST /webhook` on :8000) THROUGH a ClusterIP Service, so load
// spreads across the connection's autoscaled consumer replicas. Each accepted
// request (HTTP 202) is queued for publish onto NATS; the producer delivers it
// to the sink subject. Unlike the compose harness this runs INSIDE the cluster
// (as a k6 Job) and writes its summary to stdout — there is no in-cluster
// Prometheus to cross-check, so delivery is confirmed separately by counting the
// sink subject.
//
// Env:
//   TARGET_URL   full URL, e.g. http://consumer-lb.vrsky-platform:8000/webhook (required)
//   RATE         target requests/sec           (default 2000)
//   DURATION     constant-rate duration         (default 30s)
//   PREALLOC_VUS pre-allocated VUs              (default 100)
//   MAX_VUS      VU ceiling                     (default 800)
import http from 'k6/http';
import { check } from 'k6';

const TARGET_URL = __ENV.TARGET_URL;
if (!TARGET_URL) {
  throw new Error('TARGET_URL is required (e.g. http://consumer-lb.vrsky-platform:8000/webhook)');
}
const RATE = parseInt(__ENV.RATE || '2000', 10);
const DURATION = __ENV.DURATION || '30s';
const PREALLOC = parseInt(__ENV.PREALLOC_VUS || '100', 10);
const MAX_VUS = parseInt(__ENV.MAX_VUS || '800', 10);

export const options = {
  discardResponseBodies: true,
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(95)', 'p(99)'],
  // Guard rails so a misconfigured run (wrong URL, non-202 responses) exits
  // non-zero instead of printing a misleading "success" summary. The consumer
  // answers 202 on accept (fire-and-forget), so the 202 check reflects ingress
  // health; downstream backpressure drops are expected and measured via the
  // sink, not here.
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
  scenarios: {
    webhook: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: PREALLOC,
      maxVUs: MAX_VUS,
    },
  },
};

export default function () {
  const body = JSON.stringify({
    event: 'order.created',
    ts: Date.now(),
    vu: __VU,
    iter: __ITER,
    payload: { id: `${__VU}-${__ITER}`, amount: 42, currency: 'GBP' },
  });
  const res = http.post(TARGET_URL, body, { headers: { 'Content-Type': 'application/json' } });
  check(res, { 'accepted (202)': (r) => r.status === 202 });
}

export function handleSummary(data) {
  const dur = data.metrics.http_req_duration ? data.metrics.http_req_duration.values : {};
  const reqs = data.metrics.http_reqs ? data.metrics.http_reqs.values : {};
  const failed = data.metrics.http_req_failed ? data.metrics.http_req_failed.values : {};
  const line =
    `RESULT k8s-webhook: ${Math.round(reqs.rate || 0)} req/s sustained, ` +
    `med=${(dur.med || 0).toFixed(1)}ms p95=${(dur['p(95)'] || 0).toFixed(1)}ms ` +
    `p99=${(dur['p(99)'] || 0).toFixed(1)}ms, ${reqs.count || 0} reqs, ` +
    `failed=${((failed.rate || 0) * 100).toFixed(2)}%`;
  return { stdout: '\n' + line + '\n' };
}
