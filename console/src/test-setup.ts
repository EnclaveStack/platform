import '@testing-library/jest-dom/vitest';
import { configure } from '@testing-library/react';

// findBy…/waitFor give up after 1 s by default. A shared CI runner can take longer than that to render a
// route after sign-in (machines-flows failed there at ~1.5 s and passed locally), so wait up to 5 s: a real
// failure still fails, just a little later.
configure({ asyncUtilTimeout: 5000 });
