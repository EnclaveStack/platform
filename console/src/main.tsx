import '@enclavestack/ui/styles.css';

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { createApi, resolveApiMode } from './api/client';
import { ApiProvider } from './api/context';
import { SessionProvider } from './auth/session';

// Mock mode is for offline demos: ?first-run walks the setup flow; the demo seed fills every section.
const params = new URLSearchParams(window.location.search);
const api = createApi(resolveApiMode(import.meta.env), { setupRequired: params.has('first-run'), seed: 'demo' });
const root = document.getElementById('root');
if (!root) throw new Error('#root is missing from index.html');

createRoot(root).render(
  <StrictMode>
    <ApiProvider api={api}>
      <SessionProvider>
        <App />
      </SessionProvider>
    </ApiProvider>
  </StrictMode>,
);
