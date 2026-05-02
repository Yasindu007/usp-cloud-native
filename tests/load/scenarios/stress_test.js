// ============================================================
// stress_test.js — Find the breaking point
//
// Purpose:
//   Incrementally increase load until the system degrades.
//   This tells us:
//     1. Maximum sustainable RPS before SLO violation
//     2. Which component breaks first (DB pool? Redis? CPU?)
//     3. Whether the system recovers after load is removed
//     4. HPA reaction time under extreme load
//
// NOT a pass/fail test — it's a capacity characterisation test.
// Results inform capacity planning and HPA configuration.
//
// Interpretation:
//   The "knee" of the latency curve (where P99 starts rising sharply)
//   is the practical capacity limit. Set HPA scale-out trigger ~20%
//   before the knee.
// ============================================================

import { sleep } from 'k6';
import { doRedirect, checkRedirectResponse } from '../lib/helpers.js';
import { SEEDED_SHORT_CODES } from '../lib/config.js';
import { STRESS_THRESHOLDS }  from '../lib/thresholds.js';

export const options = {
    stages: [
        { duration: '1m',  target: 50   },
        { duration: '1m',  target: 100  },
        { duration: '1m',  target: 200  },
        { duration: '1m',  target: 300  },
        { duration: '1m',  target: 500  },   // Expect degradation here on local
        { duration: '1m',  target: 700  },   // Likely SLO breach on local Kind
        { duration: '1m',  target: 1000 },   // Pushing beyond reasonable limits
        { duration: '2m',  target: 100  },   // Recovery: does it bounce back?
    ],
    thresholds: STRESS_THRESHOLDS,
    // Do NOT abort on failure — we want to see how far we can push
    tags: { test_name: 'stress_test' },
};

export default function () {
    const code = SEEDED_SHORT_CODES[Math.floor(Math.random() * SEEDED_SHORT_CODES.length)];
    const res  = doRedirect(code);

    // In stress tests we do NOT fail on high latency — we're measuring
    // the latency at each load level. checkRedirectResponse would fail the
    // test at high load because it checks < 50ms.
    // Instead, we just record whether we got a valid response.
    const isValid = res.status === 302 || res.status === 404;
    if (!isValid) {
        // Error counter incremented automatically via errorRate in doRedirect
    }

    sleep(0.001); // Minimal think time — maximise RPS
}
