import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listener = { addListener() {} };
const state = {
  bridgeUrl: 'http://127.0.0.1:32145', token: 'test-token', tabId: 7, tabIds: [7],
  running: false, autoReconnect: true, useVisualProfile: true, taughtProfiles: {}, tabCapabilities: {}, tabCapabilityScans: {}
};
let pageInspections = 0;
let serviceChecks = 0;
const heartbeats = [];
const chrome = {
  runtime: { onInstalled: listener, onStartup: listener, onMessage: listener, getManifest: () => ({ version: 'test' }) },
  tabs: { onUpdated: listener, onRemoved: listener,
    get: async (id) => ({ id, title: 'Still loading', url: 'https://chatgpt.com/c/test', status: 'loading' }) },
  scripting: { executeScript: () => { pageInspections += 1; return new Promise(() => {}); } },
  storage: { local: {
    get: async (defaults) => ({ ...defaults, ...state }),
    set: async (changes) => { Object.assign(state, changes); }
  } }
};
const context = vm.createContext({
  chrome, console, URL, navigator: { userAgent: 'Chrome/151.0' }, AbortController,
  setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise,
  ContextBridgeProfiles: { forURL: (url) => url.startsWith('https://chatgpt.com/')
    ? { name: 'chatgpt', label: 'ChatGPT', selectors: {} } : null },
  fetch: async (url, options) => {
    if (url.endsWith('/v1/status')) {
      serviceChecks += 1;
      return { status: 200, ok: true, json: async () => ({ ok: true, version: 'v0.test' }) };
    }
    if (url.endsWith('/v1/browser/heartbeat')) {
      heartbeats.push(JSON.parse(options.body));
      return { status: 200, ok: true };
    }
    throw new Error(`unexpected URL ${url}`);
  }
});
vm.runInContext(source, context);
context.startHeartbeat = () => {};
context.poll = () => {};
context.discoverFreshTabs = async () => {};
context.closeFinishedOwnedTabs = async () => {};
context.scheduleTabDiagnostics = () => {};

const first = context.startPairing();
const duplicate = context.startPairing();
assert.strictEqual(first, duplicate, 'reopening the popup must not start a second connection attempt');
assert.equal((await first).ok, true);
assert.equal(serviceChecks, 1);
assert.equal(pageInspections, 0, 'page DOM or model-picker inspection must not gate Connect');
assert.equal(state.running, true);
assert.equal(state.relayConnected, true);
assert.equal(heartbeats[0].active_tabs, 1);
assert.equal(heartbeats[0].tabs[0].profile, 'chatgpt');
assert.equal(state.connectionProgress, null);

await context.sendHeartbeatOnce('waiting', false);
assert.equal(pageInspections, 0, 'a normal heartbeat must not wait for a suspended AI page');
assert.ok(heartbeats.length >= 2);

chrome.scripting.executeScript = async ({ func }) => {
  if (func.name === 'watchPageCapabilityInteractions') return [{ result: true }];
  if (func.name === 'safeToDiscoverPageCapabilities') return [{ result: false }];
  if (func.name === 'inspectPageCapabilities') return [{ result: { currentModel: 'GPT-5.6 Sol', currentReasoning: 'High' } }];
  if (func.name === 'inspectPageDOM') return [{ result: { prompt_inputs: 1 } }];
  throw new Error(`unexpected inspection ${func.name}`);
};
await context.refreshTabDiagnostics(7);
await context.sendHeartbeatOnce('waiting', false);
const updatedTab = heartbeats.at(-1).tabs[0];
assert.equal(updatedTab.current_model, 'GPT-5.6 Sol');
assert.equal(updatedTab.current_reasoning, 'High');
assert.equal(updatedTab.dom.prompt_inputs, 1);

// A user-initiated model change forces the authoritative picker scan instead
// of retaining the old cached model until the normal 30-minute scan.
chrome.scripting.executeScript = async ({ func }) => {
  if (func.name === 'safeToDiscoverPageCapabilities') return [{ result: true }];
  if (func.name === 'discoverPageCapabilities') return [{ result: {
    currentModel: 'GPT-5.5', currentReasoning: 'Sehr hoch', models: ['GPT-5.5'],
    reasoningLevels: ['Sehr hoch'], scanDiagnostic: { model: 'composer trigger', reasoning: 'composer trigger' }
  } }];
  if (func.name === 'inspectPageCapabilities') return [{ result: {
    currentModel: 'GPT-5.6 Sol', currentReasoning: 'Mittel'
  } }];
  if (func.name === 'inspectPageDOM') return [{ result: { prompt_inputs: 1 } }];
  throw new Error(`unexpected inspection ${func.name}`);
};
await context.refreshTabDiagnostics(7, true);
assert.equal(state.tabCapabilities[7].currentModel, 'GPT-5.5');
assert.equal(state.tabCapabilities[7].currentReasoning, 'Sehr hoch');
assert.equal(heartbeats.at(-1).tabs[0].current_model, 'GPT-5.5');

chrome.scripting.executeScript = async ({ func }) => {
  if (func.name === 'inspectPageCapabilities') return [{ result: {
    currentModel: '', currentModelVersion: '5.6', currentReasoning: 'Hoch'
  } }];
  if (func.name === 'inspectPageDOM') return [{ result: { prompt_inputs: 1 } }];
  throw new Error(`unexpected inspection ${func.name}`);
};
await context.refreshTabDiagnostics(7);
assert.equal(state.tabCapabilities[7].currentModel, '', 'a mismatched compact version must invalidate stale model metadata');
assert.equal(state.tabCapabilities[7].currentReasoning, 'Hoch');

let interactionScans = 0;
const originalTimeout = context.setTimeout;
context.setTimeout = (fn) => { fn(); return 1; };
context.scheduleTabDiagnostics = (_tabId, force) => { if (force) interactionScans += 1; };
assert.equal((await context.notePageCapabilityInteraction({ tab: { id: 7, url: 'https://chatgpt.com/c/test' } })).ok, true);
assert.equal(interactionScans, 1);
assert.equal((await context.notePageCapabilityInteraction({ tab: { id: 7, url: 'https://bugcrowd.com/' } })).ok, false);
assert.equal(interactionScans, 1, 'another page must not trigger a scan of the attached tab');
context.setTimeout = originalTimeout;

// A transient outage keeps the user's requested connection but pauses work
// until a heartbeat succeeds. With automatic reconnect disabled, three
// failures end that intent and require a fresh deliberate Connect.
context.fetch = async () => { throw new Error('service offline'); };
await context.sendHeartbeatOnce('waiting');
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, true);
assert.equal(state.relayConnected, false);
context.fetch = async (url) => url.endsWith('/v1/browser/heartbeat')
  ? { status: 200, ok: true } : { status: 200, ok: true, json: async () => ({ ok: true }) };
await context.sendHeartbeatOnce('waiting');
assert.equal(state.relayConnected, true);
state.autoReconnect = false;
context.fetch = async () => { throw new Error('service offline'); };
await context.sendHeartbeatOnce('waiting');
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, true);
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, false);
assert.equal(state.relayConnected, false);
assert.match(state.connectionError, /Automatic reconnect is off/);

state.running = true;
state.relayConnected = true;
await context.resume(true);
assert.equal(state.running, false, 'an extension update must honor the disabled automatic reconnect preference');
assert.match(state.connectionError, /Automatic reconnect is off/);

state.autoReconnect = true;
state.running = true;
state.relayConnected = true;
vm.runInContext('stopRequested = false', context);
context.fetch = async () => ({ status: 401, ok: false });
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, false, 'a rejected pairing token must halt automatic retries');
assert.equal(state.relayConnected, false);
assert.match(state.connectionError, /pairing token/i);

state.running = true;
state.relayConnected = true;
vm.runInContext('stopRequested = false', context);
context.fetch = async () => ({ status: 400, ok: false });
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, true, 'a version mismatch must retry after a service update');
assert.equal(state.relayConnected, false);
assert.match(state.connectionError, /HTTP 400.*retry/i);
context.fetch = async () => ({ status: 200, ok: true });
await context.sendHeartbeatOnce('waiting');
assert.equal(state.relayConnected, true);
assert.equal(state.connectionError, '');

state.running = false;
state.relayConnected = false;
vm.runInContext('stopRequested = false', context);
context.fetch = async (url) => url.endsWith('/v1/status')
  ? { status: 200, ok: true, json: async () => ({ ok: true, version: 'v0.test' }) }
  : { status: 400, ok: false };
await assert.rejects(context.startPairing(), /HTTP 400.*retry/i,
  'a first-connect compatibility error must not be mislabeled as a bad pairing token');
assert.equal(state.running, false);

state.running = true;
vm.runInContext('stopRequested = false', context);
context.fetch = async () => ({ status: 200, ok: true });
await context.stopPairing();
await context.resume(true);
assert.equal(state.running, false, 'a deliberate Disconnect must survive an extension update');

assert.match(fs.readFileSync(new URL('../src/popup.html', import.meta.url), 'utf8'), /id="auto-reconnect"/);

// Model a Chromium MV3 worker as disposable: storage and alarms survive a
// worker suspension, while every JavaScript global is recreated. A browser
// restart may additionally remove the alarm. Both paths must make a fresh
// heartbeat without opening the popup.
{
  const persisted = {
    bridgeUrl: 'http://127.0.0.1:32145', token: 'persisted-token', tabId: 17, tabIds: [17],
    running: true, relayConnected: true, autoReconnect: true, useVisualProfile: true,
    taughtProfiles: {}, tabCapabilities: {}, tabCapabilityScans: {}
  };
  const alarmState = { current: undefined, creates: 0, gets: 0, clears: 0 };
  const makeWorker = ({ deferHeartbeat = false, heartbeatStatus = 200, deferFirstStorageGet = false,
    deferRunningTrueSet = false } = {}) => {
    let startupListener;
    let alarmListener;
    let heartbeatStarted = 0;
    const heartbeatStates = [];
    let releaseHeartbeat;
    const heartbeatGate = deferHeartbeat ? new Promise((resolve) => { releaseHeartbeat = resolve; }) : null;
    let storageGets = 0;
    let releaseStorageGet;
    const storageGetGate = deferFirstStorageGet ? new Promise((resolve) => { releaseStorageGet = resolve; }) : null;
    let runningTrueSets = 0;
    let releaseRunningTrueSet;
    const runningTrueSetGate = deferRunningTrueSet ? new Promise((resolve) => { releaseRunningTrueSet = resolve; }) : null;
    const inert = { addListener() {} };
    const workerChrome = {
      runtime: {
        onInstalled: inert,
        onStartup: { addListener(fn) { startupListener = fn; } },
        onMessage: inert,
        getManifest: () => ({ version: 'test' })
      },
      tabs: {
        onUpdated: inert,
        onRemoved: inert,
        get: async (id) => ({ id, title: 'Persistent test tab', url: 'https://chatgpt.com/c/persistent' })
      },
      scripting: { executeScript: async () => [] },
      storage: { local: {
        get: async (defaults) => {
          const snapshot = { ...defaults, ...persisted };
          storageGets += 1;
          if (storageGetGate && storageGets === 1) await storageGetGate;
          return snapshot;
        },
        set: async (changes) => {
          if (runningTrueSetGate && changes.running === true && ++runningTrueSets === 1) await runningTrueSetGate;
          Object.assign(persisted, changes);
        }
      } },
      alarms: {
        onAlarm: { addListener(fn) { alarmListener = fn; } },
        get: async (name) => {
          alarmState.gets += 1;
          return alarmState.current?.name === name ? alarmState.current : undefined;
        },
        create: async (name, schedule) => {
          alarmState.creates += 1;
          alarmState.current = { name, ...schedule };
        },
        clear: async (name) => {
          alarmState.clears += 1;
          if (alarmState.current?.name === name) alarmState.current = undefined;
          return true;
        }
      }
    };
    const workerContext = vm.createContext({
      chrome: workerChrome, console, URL, navigator: { userAgent: 'Chrome/151.0' }, AbortController,
      setTimeout, clearTimeout, setInterval: () => 1, clearInterval() {}, Date, Promise,
      ContextBridgeProfiles: { forURL: (url) => url.startsWith('https://chatgpt.com/')
        ? { name: 'chatgpt', label: 'ChatGPT', selectors: {} } : null },
      fetch: async (url, options) => {
        if (url.endsWith('/v1/status')) {
          return { status: 200, ok: true, json: async () => ({ ok: true, version: 'v0.test' }) };
        }
        if (!url.endsWith('/v1/browser/heartbeat')) throw new Error(`unexpected lifecycle URL ${url}`);
        heartbeatStarted += 1;
        heartbeatStates.push(JSON.parse(options.body).state);
        if (heartbeatGate) await heartbeatGate;
        return { status: heartbeatStatus, ok: heartbeatStatus >= 200 && heartbeatStatus < 300 };
      }
    });
    vm.runInContext(source, workerContext);
    workerContext.poll = () => {};
    workerContext.discoverFreshTabs = async () => {};
    workerContext.closeFinishedOwnedTabs = async () => {};
    workerContext.scheduleTabDiagnostics = () => {};
    return {
      context: workerContext,
      startup: () => startupListener(),
      alarm: (alarm) => alarmListener(alarm),
      heartbeatStarted: () => heartbeatStarted,
      heartbeatStates,
      releaseHeartbeat,
      storageGets: () => storageGets,
      releaseStorageGet,
      runningTrueSets: () => runningTrueSets,
      releaseRunningTrueSet
    };
  };

  const first = makeWorker({ deferHeartbeat: true });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(first.heartbeatStarted(), 0,
    'loading a fresh worker alone must not reconnect before Chromium identifies the lifecycle event');
  assert.equal(alarmState.creates, 0,
    'loading a fresh worker alone must not recreate an alarm before lifecycle policy is known');
  let startupSettled = false;
  const startup = first.startup().then(() => { startupSettled = true; });
  while (!first.heartbeatStarted()) await new Promise((resolve) => setImmediate(resolve));
  assert.equal(alarmState.creates, 1, 'startup must recreate a missing heartbeat alarm');
  assert.equal(startupSettled, false, 'startup must retain its reconnect work until the heartbeat attempt settles');
  first.releaseHeartbeat();
  await startup;
  assert.equal(persisted.relayConnected, true);

  const createsBeforeSuspension = alarmState.creates;
  const resumed = makeWorker();
  await resumed.alarm({ name: 'contextbridge-heartbeat' });
  assert.equal(resumed.heartbeatStarted(), 1, 'an alarm must reconnect a fresh suspended worker');
  assert.equal(alarmState.creates, createsBeforeSuspension, 'an existing alarm must not be rescheduled on every wake');

  // Chromium documents that alarms can disappear across a browser restart.
  // Persisted connection intent must therefore be reconciled against the API,
  // rather than a process-local `heartbeatAlarmRegistered` flag.
  alarmState.current = undefined;
  const restarted = makeWorker();
  await restarted.startup();
  assert.equal(restarted.heartbeatStarted(), 1, 'browser startup must retry the persisted connection intent');
  assert.equal(alarmState.creates, createsBeforeSuspension + 1, 'browser startup must recreate an alarm removed by restart');
  assert.ok(alarmState.gets >= 3, 'each fresh worker lifecycle must verify persistent alarm state');

  persisted.running = true;
  persisted.relayConnected = true;
  persisted.autoReconnect = false;
  const disabled = makeWorker();
  await disabled.startup();
  assert.equal(persisted.running, false, 'startup must honor an explicitly disabled automatic reconnect');
  assert.equal(alarmState.current, undefined, 'disabled reconnect must remove the persistent wake alarm');
  assert.deepEqual(disabled.heartbeatStates, ['paused'], 'disabled reconnect may only unregister the prior browser session');

  persisted.running = true;
  persisted.relayConnected = true;
  persisted.autoReconnect = true;
  const rejected = makeWorker({ heartbeatStatus: 401 });
  await rejected.startup();
  assert.equal(persisted.running, false, 'an authentication rejection must not enter an automatic retry loop');
  assert.equal(persisted.relayConnected, false);
  assert.equal(alarmState.current, undefined, 'an authentication rejection must remove the wake alarm');
  assert.deepEqual(rejected.heartbeatStates, ['waiting']);

  // A Disconnect can overtake an MV3 resume whose storage read already
  // captured the old running=true snapshot. The stale continuation must not
  // recreate the alarm, publish waiting, or mark the relay connected again.
  persisted.running = true;
  persisted.relayConnected = true;
  persisted.autoReconnect = true;
  alarmState.current = { name: 'contextbridge-heartbeat', periodInMinutes: 0.5 };
  const raced = makeWorker({ deferFirstStorageGet: true });
  const staleResume = raced.context.resume();
  while (!raced.storageGets()) await new Promise((resolve) => setImmediate(resolve));
  const createsBeforeDisconnect = alarmState.creates;
  await raced.context.stopPairing();
  assert.equal(persisted.running, false);
  assert.equal(persisted.relayConnected, false);
  assert.equal(alarmState.current, undefined);
  raced.releaseStorageGet();
  await staleResume;
  assert.equal(persisted.running, false, 'a stale resume must not reverse an explicit Disconnect');
  assert.equal(persisted.relayConnected, false, 'a stale resume heartbeat must not reconnect the relay');
  assert.equal(alarmState.current, undefined, 'a stale resume must not recreate the heartbeat alarm');
  assert.equal(alarmState.creates, createsBeforeDisconnect, 'a stale resume must not schedule another alarm');
  assert.deepEqual(raced.heartbeatStates, ['paused'], 'Disconnect must be the final lifecycle heartbeat');

  // Storage writes can complete out of invocation order. If Connect's
  // running=true write is delayed past Disconnect's running=false write, the
  // stale Connect must not leave persisted reconnect intent behind.
  persisted.running = false;
  persisted.relayConnected = false;
  persisted.autoReconnect = true;
  alarmState.current = undefined;
  const delayedStart = makeWorker({ deferRunningTrueSet: true });
  const connecting = delayedStart.context.startPairing();
  while (!delayedStart.runningTrueSets()) await new Promise((resolve) => setImmediate(resolve));
  const disconnecting = delayedStart.context.stopPairing();
  delayedStart.releaseRunningTrueSet();
  const [connectResult, disconnectResult] = await Promise.allSettled([connecting, disconnecting]);
  assert.equal(connectResult.status, 'rejected', 'Disconnect must cancel the older Connect continuation');
  assert.equal(disconnectResult.status, 'fulfilled');
  assert.equal(persisted.running, false, 'the final persisted lifecycle intent must remain disconnected');
  assert.equal(persisted.relayConnected, false);
  assert.equal(alarmState.current, undefined);
  assert.deepEqual(delayedStart.heartbeatStates, ['paused'], 'a cancelled Connect must not publish waiting');
  await delayedStart.context.resume();
  assert.equal(delayedStart.heartbeatStarted(), 1, 'persisted Disconnect must prevent a later automatic reconnect');
}
