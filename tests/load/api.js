// API load for tests/load/run.py: a constant arrival rate of authenticated
// reads, signed in once in setup() the way a real client reuses its token.
// Run by the pinned grafana/k6 image; settings come from the environment.
import http from 'k6/http';
import { check, fail } from 'k6';

const base = __ENV.API_BASE_URL || 'http://10.99.0.20:8080';
const rate = Number(__ENV.API_RATE || 100);
const duration = __ENV.DURATION || '60s';
const path = __ENV.API_PATH || '/api/v1/accounts';

export const options = {
  discardResponseBodies: true,
  insecureSkipTLSVerify: true,
  summaryTrendStats: ['p(50)', 'p(90)', 'p(95)', 'p(99)', 'max', 'avg'],
  scenarios: {
    api: {
      executor: 'constant-arrival-rate',
      rate: rate,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: Math.max(10, rate),
      maxVUs: Math.max(50, rate * 5),
    },
  },
};

export function setup() {
  const res = http.post(`${base}/api/v1/auth/login`,
    JSON.stringify({ email: __ENV.LOAD_USER, password: __ENV.LOAD_PASSWORD }),
    { headers: { 'Content-Type': 'application/json' }, responseType: 'text' });
  const cookie = res.cookies.restmail_access;
  if (res.status !== 200 || !cookie || !cookie.length) {
    fail(`sign-in as ${__ENV.LOAD_USER} failed with ${res.status}`);
  }
  return { token: cookie[0].value };
}

export default function (data) {
  const res = http.get(`${base}${path}`, { headers: { Authorization: `Bearer ${data.token}` } });
  check(res, { 'status 200': (r) => r.status === 200 });
}
