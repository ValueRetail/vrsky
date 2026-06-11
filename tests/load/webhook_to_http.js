// Flagship load scenario: webhook ingress → NATS → http-producer → httpbin.
//
// k6 drives a constant arrival rate of signed-shaped JSON POSTs at the
// webhook-consumer's /webhook/{connectionId} ingress. Each accepted request
// (HTTP 202) publishes one message onto NATS, which the http-producer delivers
// to the sink. k6 measures ingress accept latency (http_req_duration) and the
// sustained request rate; run.sh cross-checks the NATS-published count via the
// vrsky_messages_published_total counter in Prometheus.
//
// Driven entirely by env vars so the same script serves the local run and the
// CI smoke (short duration + a p99 ceiling threshold):
//   WEBHOOK_URL      full URL of /webhook/{id} (required)
//   RATE             target requests/sec            (default 200)
//   DURATION         constant-rate duration         (default 30s)
//   PREALLOC_VUS     pre-allocated VUs              (default 50)
//   MAX_VUS          VU ceiling                     (default 300)
//   P99_CEILING_MS   if >0, fail the run when p99 exceeds it (CI guard)
//   MAX_FAILED_RATE  allowed http_req_failed rate   (default 0.01)
import http from 'k6/http';
import { check } from 'k6';

const WEBHOOK_URL = __ENV.WEBHOOK_URL;
const RATE = parseInt(__ENV.RATE || '200', 10);
const DURATION = __ENV.DURATION || '30s';
const PREALLOC = parseInt(__ENV.PREALLOC_VUS || '50', 10);
const MAX_VUS = parseInt(__ENV.MAX_VUS || '300', 10);
const P99_CEIL = parseInt(__ENV.P99_CEILING_MS || '0', 10);
const MAX_FAILED = __ENV.MAX_FAILED_RATE || '0.01';

const thresholds = {
  http_req_failed: [`rate<${MAX_FAILED}`],
};
if (P99_CEIL > 0) {
  thresholds.http_req_duration = [`p(99)<${P99_CEIL}`];
}

export const options = {
  discardResponseBodies: true,
  // k6's default trend stats omit p(99) — request it explicitly so handleSummary
  // and the p99 threshold have a value to read.
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(95)', 'p(99)'],
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
  thresholds,
};

export default function () {
  const body = JSON.stringify({
    event: 'order.created',
    ts: Date.now(),
    vu: __VU,
    iter: __ITER,
    payload: { id: `${__VU}-${__ITER}`, amount: 42, currency: 'GBP' },
  });
  const res = http.post(WEBHOOK_URL, body, {
    headers: { 'Content-Type': 'application/json' },
  });
  // The webhook ingress acknowledges with 202 Accepted once the message is
  // published to NATS. Anything else is a real failure for this scenario.
  check(res, { 'webhook accepted (202)': (r) => r.status === 202 });
}

// handleSummary writes a compact machine-readable summary that run.sh parses,
// and a short human line to stdout. Using handleSummary (not the deprecated
// --summary-export) keeps us forward-compatible with current k6.
export function handleSummary(data) {
  const dur = data.metrics.http_req_duration ? data.metrics.http_req_duration.values : {};
  const reqs = data.metrics.http_reqs ? data.metrics.http_reqs.values : {};
  const failed = data.metrics.http_req_failed ? data.metrics.http_req_failed.values : {};
  const out = {
    med: dur.med || 0,
    p95: dur['p(95)'] || 0,
    p99: dur['p(99)'] || 0,
    avg: dur.avg || 0,
    max: dur.max || 0,
    count: reqs.count || 0,
    rate: reqs.rate || 0,
    failed_rate: failed.rate || 0,
  };
  const line =
    `webhook→http: ${Math.round(out.rate)} req/s sustained, ` +
    `med=${out.med.toFixed(1)}ms p95=${out.p95.toFixed(1)}ms ` +
    `p99=${out.p99.toFixed(1)}ms, ${out.count} reqs, ` +
    `failed=${(out.failed_rate * 100).toFixed(2)}%\n`;
  return {
    '/out/summary.json': JSON.stringify(out, null, 2),
    stdout: '\n' + line,
  };
}
