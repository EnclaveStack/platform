import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { routes } from './App';
import { ApiProvider } from './api/context';
import { createMockApi, DEMO_ADMIN, type MockOptions } from './api/mock';
import { SessionProvider } from './auth/session';

// One way for the flow tests to run the console: real routes and components on the mock adapter.

export function renderConsole(path: string, options: MockOptions = {}) {
  const api = createMockApi({ latencyMs: 0, stepMs: 30, storage: null, ...options });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <ApiProvider api={api}>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </ApiProvider>,
  );
  return { router, api, user: userEvent.setup() };
}

export async function signIn(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText('Email'), DEMO_ADMIN.email);
  await user.type(screen.getByLabelText('Password'), DEMO_ADMIN.password);
  await user.click(screen.getByRole('button', { name: 'Sign in' }));
}
