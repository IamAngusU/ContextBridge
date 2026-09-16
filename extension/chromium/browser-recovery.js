// ContextBridge MV3 observation continuation.
//
// A Chromium service worker can be suspended after an irreversible provider
// action while the isolated page script is still waiting for the answer. The
// durable browser claim deliberately contains no prompt text, but it does prove
// the exact job generation, session, execution tab, conversation and baseline.
// This companion script turns only a recovered sent-unknown claim into a
// durable handoff. The existing queue then leases the same job again as
// observation-only work, so no provider action can be repeated.
(() => {
  let continuation = Promise.resolve();
  const coreProcessWork = processWork;

  function claimIdentityMatches(current, expected) {
    return Boolean(current && expected
      && Number(current.generation) === Number(expected.generation)
      && Number(current.tabId) === Number(expected.tabId)
      && String(current.sessionKey || '') === String(expected.sessionKey || '')
      && String(current.expectedURL || '') === String(expected.expectedURL || ''));
  }

  async function markObservationHandoff(lease) {
    return serializeBrowserClaimWrite(async () => {
      const cfg = await settings();
      const current = cfg.browserJobClaims?.[lease.jobId];
      if (!claimIdentityMatches(current, lease) || current.state !== 'sent_unknown') return false;
      const browserJobClaims = {
        ...cfg.browserJobClaims,
        [lease.jobId]: { ...current, state: 'observation_handoff' }
      };
      await api.storage.local.set({ browserJobClaims });
      return true;
    });
  }

  function handoffClaimCanOwnTab(cfg, jobId, claim, tab) {
    if (!claim || claim.state !== 'observation_handoff') return false;
    // Reuse the production recovery proof, but present the handoff as the
    // sent-unknown state it came from. This performs no provider or bridge I/O.
    return Boolean(recoveredLeaseFromClaim(cfg, jobId, { ...claim, state: 'sent_unknown' }, tab));
  }

  async function adoptObservationLease(work) {
    const jobId = String(work?.job?.id || '');
    const generation = Number(work?.lease_generation || 0);
    if (!jobId || work?.observation_only !== true || !Number.isSafeInteger(generation) || generation <= 0) return 0;

    let cfg = await settings();
    let claim = cfg.browserJobClaims?.[jobId];
    if (!claim || claim.state !== 'observation_handoff') return 0;
    const previousGeneration = Number(claim.generation || 0);
    const tabId = Number(claim.tabId || 0);
    if (!Number.isSafeInteger(previousGeneration) || previousGeneration <= 0 || generation <= previousGeneration
        || !Number.isInteger(tabId) || tabId <= 0 || !configuredTabIDs(cfg).includes(tabId)) return 0;

    const tab = await api.tabs.get(tabId).catch(() => null);
    if (!tab?.url || !handoffClaimCanOwnTab(cfg, jobId, claim, tab)) return 0;

    const adopted = await serializeBrowserClaimWrite(async () => {
      const latest = await settings();
      const current = latest.browserJobClaims?.[jobId];
      if (!claimIdentityMatches(current, claim) || current.state !== 'observation_handoff') return false;
      const deadline = Date.parse(String(work?.deadline || ''));
      const browserJobClaims = {
        ...latest.browserJobClaims,
        [jobId]: {
          ...current,
          generation,
          state: 'sent_unknown',
          at: Date.now(),
          ...(Number.isFinite(deadline) && deadline > 0 ? { deadline: new Date(deadline).toISOString() } : {})
        }
      };
      await api.storage.local.set({ browserJobClaims });
      return true;
    });
    if (!adopted) return 0;

    // The local queue may still carry the relay-selected launcher tab. The
    // durable claim is stronger evidence for observation because it was written
    // only after resolveWorkTab selected the concrete execution conversation.
    work.job = { ...work.job, contextbridge_browser_tab_id: tabId };
    return tabId;
  }

  // Observation-only re-leases may be delivered through the original routing
  // poller. Redirect only those already-sent jobs to the exact tab proven by
  // the durable claim. Initial jobs and all side-effecting work keep the normal
  // processWork path unchanged.
  processWork = async function processWorkWithMV3Continuation(cfg, work, claimedTabId) {
    if (work?.observation_only === true) {
      const recoveredTabId = await adoptObservationLease(work);
      if (recoveredTabId > 0) return coreProcessWork(await settings(), work, recoveredTabId);
    }
    return coreProcessWork(cfg, work, claimedTabId);
  };

  async function handoffRecoveredSentUnknownClaims() {
    await waitForBrowserLeaseRecovery();
    const gate = beginBrowserLeaseRecovery();
    try {
      const generation = lifecycleGeneration;
      const cfg = await settings();
      if (!cfg.running || stopRequested) return;
      await restorePersistedBrowserLeaseReservations(generation);
      if (generation !== lifecycleGeneration || stopRequested) return;

      const recovered = [...activeBrowserLeases.values()].filter((lease) =>
        lease?.recovered && lease.sentUnknown && !lease.cancelled);
      for (const lease of recovered) {
        if (generation !== lifecycleGeneration || stopRequested) return;
        const reservation = recoveredTabReservations.get(lease.tabId);
        if (!reservation || reservation.token !== lease.reservationToken || reservation.quarantined) continue;
        if (!await markObservationHandoff(lease)) continue;

        // Once the durable handoff is recorded, the old generation may no
        // longer authorize provider work. Releasing it merely accelerates the
        // observation-only re-lease; if transport is unavailable, the lease
        // expires normally and the same queue semantics take over later.
        try { await releaseBrowserLease(await settings(), lease.jobId, lease.generation); }
        catch (_) { /* Transport uncertainty is resolved by normal lease expiry. */ }
        releaseRecoveredBrowserLease(lease);
      }
    } finally {
      finishBrowserLeaseRecovery(gate);
      const cfg = await settings().catch(() => null);
      if (cfg?.running && !stopRequested) void poll();
    }
  }

  function scheduleRecoveredObservationHandoff() {
    continuation = continuation.catch(() => {}).then(handoffRecoveredSentUnknownClaims);
    return continuation;
  }

  // These listeners are registered after background.js, so its resume handler
  // establishes the normal lifecycle policy first. The serialized continuation
  // waits for that recovery gate before changing a sent-unknown claim.
  api.runtime.onInstalled?.addListener(() => { void scheduleRecoveredObservationHandoff(); });
  api.runtime.onStartup?.addListener(() => { void scheduleRecoveredObservationHandoff(); });
  api.alarms?.onAlarm?.addListener((alarm) => {
    if (alarm?.name === HEARTBEAT_ALARM) void scheduleRecoveredObservationHandoff();
  });
  api.runtime.onMessage?.addListener((message) => {
    if (message?.type === 'authorize-browser-job-action' && message?.action === 'observe') {
      // A provider-tab message itself can be the event that wakes an MV3
      // worker. Let the core authorization finish first, then hand off only if
      // it reconstructed a recovered sent-unknown lease.
      setTimeout(() => { void scheduleRecoveredObservationHandoff(); }, 0);
    }
    return false;
  });

  // Small, content-free hooks for deterministic regression tests. They expose
  // no credentials, prompts, page URLs or runtime state.
  globalThis.ContextBridgeBrowserRecovery = Object.freeze({
    adoptObservationLease,
    handoffRecoveredSentUnknownClaims,
    scheduleRecoveredObservationHandoff
  });
})();
