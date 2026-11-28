import { DesignBlocked } from './provider.mjs';

// Retry an unavailable renderer on normal polls; never retain a failed runner.
export async function refreshRenderer(service, createRunner) {
  try {
    const runner = createRunner();
    const runtime = await runner.probe();
    service.renderer = runtime;
    service.visualParity = 'ready';
    service.rendererProbedAt = new Date().toISOString();
    delete service.visualParityReason;
    return runner;
  } catch {
    delete service.renderer;
    service.visualParity = 'unavailable';
    service.visualParityReason = 'Isolated renderer runtime or Chromium probe failed';
    return undefined;
  }
}

// Probe in the background, allowing provider and nonvisual work to continue.
export function rendererController(service, createRunner, signal) {
  let runner, probing;
  const unavailable = () => {
    runner = undefined;
    delete service.renderer;
    service.visualParity = 'unavailable';
    service.visualParityReason = 'Isolated renderer execution could not be confirmed';
  };
  return {
    get current() { return runner; },
    start() {
      if (runner || probing || signal?.aborted) return;
      probing = refreshRenderer(service, () => createRunner(unavailable))
        .then(found => {
          if (signal?.aborted) { unavailable(); return; }
          if (!found) { runner = undefined; return; }
          runner = { ...found, async measure(...args) {
            try { return await found.measure(...args); }
            catch (error) { if (!(error instanceof DesignBlocked)) unavailable(); throw error; }
          } };
        }).finally(() => { probing = undefined; });
    },
    async wait() { await probing; },
  };
}

// One-shot mode must consume the completed probe before reporting success.
export async function rendererPoll(controller, poll, once) {
  controller.start();
  if (once) await controller.wait();
  const ok = await poll();
  return Boolean(ok && (!once || controller.current));
}

export function pollComplete(observation, fleetEvidence, results) {
  return observation.provider === 'ready' && fleetEvidence === 'ready' &&
    results.every(result => result.state !== 'unknown' && result.conclusion != null);
}
