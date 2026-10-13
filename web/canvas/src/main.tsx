import { initializeSiteContext } from './integration/site-context';
// Initialize user-scoped stores only after the authenticated host handshake.
void initializeSiteContext()
    .then(() => import('./integration/app'))
    .catch(() => window.parent.postMessage({ type: 'new-api:canvas-error' }, window.location.origin));
