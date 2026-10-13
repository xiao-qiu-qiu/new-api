import { initializeSiteContext } from './integration/site-context';
// Initialize user-scoped stores only after the authenticated host handshake.
void initializeSiteContext().then(() => import('./integration/app'));
