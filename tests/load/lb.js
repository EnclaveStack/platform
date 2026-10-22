// Zero-drop rolling deploys behind a load balancer (L2-006 LB-06, L5-002 §7): a constant request rate
// through the published load balancer while tests/load/lb.sh redeploys the app behind it. Any failed
// request fails the run.
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  scenarios: {
    steady: { executor: 'constant-arrival-rate', rate: Number(__ENV.RATE || 200), timeUnit: '1s', duration: __ENV.DURATION || '60s',
      preAllocatedVUs: 50, maxVUs: 200 },
  },
  thresholds: { http_req_failed: ['rate==0'], checks: ['rate==1'] },
};

export default function () {
  const res = http.get(__ENV.LB_URL, { timeout: '5s' });
  if (!check(res, { 'status is 200': (r) => r.status === 200 })) console.warn(`FAIL ${res.status} ${res.error} t=${Date.now()}`);
}
