/**
 * Soak test with memory leak detection.
 *
 * Runs the same 15 rps steady-state load as soak.js. At setup() time, queries
 * Prometheus for a baseline of the connectivity-tester-stage container memory.
 * After the soak, teardown() re-queries and fails the `checks` threshold if
 * memory grew more than MEMORY_GROWTH_THRESHOLD (default 30%).
 *
 * Env vars:
 *   SOAK_RPS               — requests/sec (default: 15)
 *   SOAK_DURATION          — total test duration (default: 30m)
 *   PROMETHEUS_URL         — Prometheus HTTP API base URL
 *   MEMORY_GROWTH_THRESHOLD — fractional threshold, e.g. 0.30 = 30% (default: 0.30)
 */

import http from 'k6/http';
import { check } from 'k6';
import { BASE_URL, SERVER_NAMES, randomItem } from './config.js';

const SOAK_RPS                = parseInt(__ENV.SOAK_RPS || '15', 10);
const SOAK_DURATION           = __ENV.SOAK_DURATION || '30m';
const PROMETHEUS_URL          = __ENV.PROMETHEUS_URL || 'http://prometheus-operated.monitoring.svc.cluster.local:9090';
const MEMORY_GROWTH_THRESHOLD = parseFloat(__ENV.MEMORY_GROWTH_THRESHOLD || '0.30');

// cAdvisor metric — excludes page cache, reflects true resident memory
const MEMORY_QUERY = 'avg(container_memory_working_set_bytes{namespace="matrix",pod=~"connectivity-tester-stage-.*",container="federation-tester-api"})';

export const options = {
  scenarios: {
    soak: {
      executor: 'constant-arrival-rate',
      exec: 'federationOk',
      rate: SOAK_RPS,
      timeUnit: '1s',
      duration: SOAK_DURATION,
      // At 15 rps × ~1.5s avg = ~23 VUs needed; allocate headroom
      preAllocatedVUs: 40,
      maxVUs: 80,
    },
  },
  thresholds: {
    http_req_failed:   ['rate<0.05'],
    // If p95 climbs above 5s during a soak that was fine at 1.2s, something is leaking
    http_req_duration: ['p(95)<5000'],
    // Memory growth check — reported via teardown() check()
    checks: ['rate==1.0'],
  },
};

function queryMemoryBytes() {
  const url = `${PROMETHEUS_URL}/api/v1/query?query=${encodeURIComponent(MEMORY_QUERY)}`;
  const res = http.get(url, { timeout: '10s', tags: { name: 'prometheus_memory_query' } });
  if (res.status !== 200) {
    console.warn(`Prometheus query failed: HTTP ${res.status}`);
    return null;
  }
  try {
    const body = JSON.parse(res.body);
    if (body.status === 'success' && body.data.result.length > 0) {
      return parseFloat(body.data.result[0].value[1]);
    }
    console.warn('Prometheus returned no results for memory query');
  } catch (e) {
    console.warn(`Failed to parse Prometheus response: ${e}`);
  }
  return null;
}

export function setup() {
  const baseline = queryMemoryBytes();
  if (baseline !== null) {
    console.log(`Baseline memory: ${(baseline / 1024 / 1024).toFixed(1)} MiB`);
  } else {
    console.warn('Could not establish baseline memory; memory growth check will be skipped in teardown');
  }
  return { baselineMemory: baseline };
}

export function federationOk() {
  const server = randomItem(SERVER_NAMES);
  const res = http.get(
    `${BASE_URL}/api/federation/federation-ok?server_name=${encodeURIComponent(server)}`,
    { timeout: '15s' },
  );
  check(res, {
    'federation-ok: status 200':  (r) => r.status === 200,
    'federation-ok: GOOD or BAD': (r) =>
      r.body.trim() === 'GOOD' || r.body.trim() === 'BAD',
  });
}

export function teardown(data) {
  if (data.baselineMemory === null) {
    console.warn('Baseline memory was unavailable; skipping memory growth check');
    return;
  }
  const current = queryMemoryBytes();
  if (current === null) {
    console.warn('Post-soak memory unavailable; skipping memory growth check');
    return;
  }

  const growth     = (current - data.baselineMemory) / data.baselineMemory;
  const baseMiB    = (data.baselineMemory / 1024 / 1024).toFixed(1);
  const currentMiB = (current / 1024 / 1024).toFixed(1);
  console.log(`Memory: baseline=${baseMiB} MiB → current=${currentMiB} MiB (${(growth * 100).toFixed(1)}% growth)`);

  check(growth, {
    [`memory growth <= ${(MEMORY_GROWTH_THRESHOLD * 100).toFixed(0)}%`]: (g) => g <= MEMORY_GROWTH_THRESHOLD,
  });
}
