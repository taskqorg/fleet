// Browser errors go to Bugsink, Fleet's error tracker (docs/designs/fleet-error-tracking.md), through the Sentry
// browser SDK. Events take Fleet's own tunnel POST /api/errors: same origin, so blockers keep them and no public ingest
// is needed; the coordinator checks the DSN and names the person (the SDK sends no personal data). main.tsx imports
// this module first and it imports no app module, so the SDK is up before any other app module runs; main.tsx then
// names what is not reported and the version (configureErrors). Without a DSN (config/bugsink.json) nothing starts.
import {
  init, captureException, breadcrumbsIntegration, browserApiErrorsIntegration, dedupeIntegration, functionToStringIntegration,
  globalHandlersIntegration, httpContextIntegration, linkedErrorsIntegration,
} from '@sentry/browser';
import bugsink from '../../config/bugsink.json';

let ignored: (e: unknown) => boolean = () => false;
let release: () => string | null = () => null;

/** What is not reported (an operation's own failure, an access change) and the Fleet version this tab runs. */
export function configureErrors(o: { ignore: (e: unknown) => boolean; release: () => string | null }) { ignored = o.ignore; release = o.release; }

if (bugsink.web) {
  init({
    dsn: bugsink.web, tunnel: '/api/errors', environment: 'production',
    // No personal data from the browser (SDK 11's form of sendDefaultPii off): no user fields, cookies or bodies.
    dataCollection: { userInfo: false, cookies: false, httpBodies: [] },
    // Errors only: no sessions, tracing or replay.
    defaultIntegrations: [functionToStringIntegration(), browserApiErrorsIntegration(), breadcrumbsIntegration(), globalHandlersIntegration(),
      linkedErrorsIntegration(), dedupeIntegration(), httpContextIntegration()],
    beforeSend(event, hint) {
      if (ignored(hint.originalException)) return null;
      // The version as system_versions and the version chip show it; unknown until the first version check.
      const v = release();
      if (v) event.release = v;
      return event;
    },
  });
}

/** A render crash caught by the app's boundary (it is not an uncaught error, so the global handlers do not see it). */
export function captureRender(error: unknown) {
  if (bugsink.web && !ignored(error)) captureException(error, { tags: { source: 'render' } });
}
