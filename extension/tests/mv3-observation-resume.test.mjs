import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { test } from 'node:test';

const source = fs.readFileSync(new URL('../src/browser-recovery.js', import.meta.url), 'utf8');

function harness({ releaseFails = false, quarantined = false } = {}) {
  const state = {
    running: true,
    tabIds: [11, 22],
    browserJobClaims: {
      job1: {
        generation: 7,
        tabId: 22,
        sessionKey: 'session-key',
        expectedURL: 'https://chatgpt.com/c/owned',
        profileName: 'chatgpt',
        state: 'sent_unknown',
        at: Date.now(),
        deadline: new Date(Date.now() + 120_000).toISOString(),
        promptProof: { nonce: 'a'.repeat(32), digest: 'b'.repeat(64) }
      }
    },
    sessionBindings: {
      'session-key': { tabId: 22, url: 'https://chatgpt.com/c/owned' }
    }
  };
  const token = Symbol('tab-22');
  const recovered = {
    jobId: 'job1', generation: 7, tabId: 22, sessionKey: 'session-key',
    expectedURL: 'https://chatgpt.com/c/owned', recovered: true,
    sentUnknown: true, cancelled: false, reservationToken: token
  };
  const ordinaryRecovered = {
    jobId: 'job2', generation: 2, tabId: 11, sessionKey: 'other',
    expectedURL: 'https://chatgpt.com/', recovered: true,
    sentUnknown: false, cancelled: false, reservationToken: Symbol('tab-11')
  };
  const activeBrowserLeases = new Map([['job1', recovered], ['job2', ordinaryRecovered]]);
  const recoveredTabReservations = new Map([
    [22, { token, claims: new Map([['job1:7', recovered]]), quarantined }],
    [11, { token: ordinaryRecovered.reservationToken, claims: new Map([['job2:2', ordinaryRecovered]]), quarantined: false }]
  ]);
  const calls = { core: [], released: [], reservationReleased: [], restored: 0, polled: 0, gates: 0 };
  const listeners = {};

  const context = vm.createContext({
    console,
    setTimeout,
    clearTimeout,
    Date,
    Promise,
    Map,
    Set,
    Object,
    Number,
    String,
    Boolean,
    Symbol,
    JSON,
    Math,
    Error,
    globalThis: null,
    stopRequested: false,
    lifecycleGeneration: 5,
    HEARTBEAT_ALARM: 'contextbridge-heartbeat',
    activeBrowserLeases,
    recoveredTabReservations,
    processWork: async (cfg, work, tabId) => {
      calls.core.push({ cfg, work: structuredClone(work), tabId });
      return { ok: true, tabId };
    },
    settings: async () => state,
    configuredTabIDs: (cfg) => cfg.tabIds,
    workSessionKey: () => 'session-key',
    recoveredLeaseDeadline: (claim) => Date.parse(claim.deadline),
    recoveredLeaseFromClaim: (cfg, jobId, claim, tab) => {
      const binding = cfg.sessionBindings?.[claim.sessionKey];
      if (jobId !== 'job1' || claim.state !== 'sent_unkown' || !binding || binding.tabId !== claim.tabId
          || binding.url !== claim.expectedURL || tab?.url !== claim.expectedURL) return null;
      return { jobId, generation: claim.generation, tabId: claim.tabId };
    },
    serializeBrowserClaimWrite: async (operation) => operation(),
    waitForBrowserLeaseRecovery: async () => {},
    beginBrowserLeaseRecovery: () => { calls.gates += 1; return { id: calls.gates }; },
    finishBrowserLeaseRecovery: () => {},
    restorePersistedBrowserLeaseReservations: async () => { calls.restored += 1; },
    releaseBrowserLease: async (_cfg, jobId, generation) => {
      calls.released.push({ jobId, generation });
      if (releaseFails) throw new Error('offline');
      return true;
    },
    releaseRecoveredBrowserLease: (lease) => {
      calls.reservationReleased.push(lease.jobId);
      activeBrowserLeases.delete(lease.jobId);
      recoveredTabReservations.delete(lease.tabId);
    },
    poll: async () => { calls.polled += 1; },
    api: {
      storage: {
        local: {
          set: async (changes) => Object.assign(state, changes)
        }
      },
      tabs: {
        get: async (tabId) => tabId === 22 ? { id: 22, url: 'https://chatgpt.com/c/owned' } : { id: tabId, url: 'https://chatgpt.com/' }
      },
      runtime: {
        onInstalled: { addListener: (fn) => { listeners.installed = fn; } },
        onStartup: { addListener: (fn) => { listeners.startup = fn; } },
        onMessage: { addListener: (fn) => { listeners.message = fn; } }
      },
      alarms: { onAlarm: { addListener: (fn) => { listeners.alarm = fn; } } }
    }
  });
  context.globalThis = context;
  vm.runInContext(source, context, { filename: 'browser-recovery.js' });
  return { context, state, calls, recovered, ordinaryRecovered, listeners };
}

test('sent-unknown MV3 recovery becomes a durable observation handoff', async () => {
  const { context, state, calls } = harness();
  await context.ContextBridgeBrowserRecovery.handoffRecoveredSentUnknownClaims();

  assert.equal(state.browserJobClaims.job1.state, 'observation_handoff');
  assert.deepEqual(calls.released, [{ jobId: 'job1', generation: 7 }]);
  assert.deepEqual(calls.reservationReleased, ['job1']);
  assert.equal(calls.restored, 1);
  assert.equal(calls.polled, 1);
  assert.equal(calls.released.some((entry) => entry.jobId === 'job2'), false,
    'a pre-action recovered lease must not be converted into observation-only work');
});

test('transport failure after durable handoff waits for normal lease expiry without resending', async () => {
  const { context, state, calls } = harness({ releaseFails: true });
  await context.ContextBridgeBrowserRecovery.handoffRecoveredSentUnknownClaims();

  assert.equal(state.browserJobClaims.job1.state, 'observation_handoff');
  assert.deepEqual(calls.released, [{ jobId: 'job1', generation: 7 }]);
  assert.deepEqual(calls.reservationReleased, ['job1']);
});

test('quarantined duplicate claims remain fail-closed', async () => {
  const { context, state, calls } = harness({ quarantined: true });
  await context.ContextBridgeBrowserRecovery.handoffRecoveredSentUnknownClaims();

  assert.equal(state.browserJobClaims.job1.state, 'sent_unknown');
  assert.equal(calls.released.length, 0);
  assert.equal(calls.reservationReleased.length, 0);
});

test('next observation lease adopts the exact execution tab and a new durable generation', async () => {
  const { context, state, calls } = harness();
  await context.ContextBridgeBrowserRecovery.handoffRecoveredSentUnknownClaims();

  const work = {
    observation_only: true,
    lease_generation: 8,
    deadline: new Date(Date.now() + 90_000).toISOString(),
    job: { id: 'job1', contextbridge_browser_tab_id: 11 }
  };
  const result = await context.processWork({ stale: true }, work, 11);

  assert.equal(result.tabId, 22);
  assert.equal(calls.core.length, 1);
  assert.equal(calls.core[0].tabId, 22);
  assert.equal(calls.core[0].work.job.contextbridge_browser_tab_id, 22);
  assert.equal(state.browserJobClaims.job1.generation, 8);
  assert.equal(state.browserJobClaims.job1.state, 'sent_unknown');
});

test('handoff reservation returns unrelated work on the execution tab untouched', async () => {
  const { context, calls } = harness();
  await context.ContextBridgeBrowserRecovery.handoffRecoveredSentUnknownClaims();

  const unrelated = { observation_only: false, lease_generation: 3, job: { id: 'other-job' } };
  const result = await context.processWork({ current: true }, unrelated, 22);

  assert.equal(result, undefined);
  assert.equal(calls.core.length, 0);
  assert.deepEqual(calls.released.at(-1), { jobId: 'other-job', generation: 3 });
});

test('a durable handoff can never execute again as side-effecting work', async () => {
  const { context, calls } = harness();
  await context.ContextBridgeBrowserRecovery.handoffRecoveredSentUnknownClaims();

  const malformed = { observation_only: false, lease_generation: 8, job: { id: 'job1', contextbridge_browser_tab_id: 11 } };
  const result = await context.processWork({ current: true }, malformed, 11);

  assert.equal(result, undefined);
  assert.equal(calls.core.length, 0);
  assert.deepEqual(calls.released.at(-1), { jobId: 'job1', generation: 8 });
});

test('ordinary work never uses durable handoff routing', async () => {
  const { context, calls } = harness();
  const work = { observation_only: false, lease_generation: 8, job: { id: 'job1', contextbridge_browser_tab_id: 11 } };
  await context.processWork({ current: true }, work, 11);
  assert.equal(calls.core[0].tabId, 11);
  assert.equal(calls.core[0].work.job.contextbridge_browser_tab_id, 11);
});
