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
