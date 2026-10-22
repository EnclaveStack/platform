import { createBrowserRouter, Navigate, RouterProvider } from 'react-router';
import { ConsoleLayout } from './routes/ConsoleLayout';
import { LoginPage } from './routes/LoginPage';
import { RequireAuth } from './routes/RequireAuth';
import { SectionPage } from './routes/SectionPage';
import { SetupPage } from './routes/SetupPage';
import { TokensPage } from './routes/TokensPage';
import { AccountPage } from './routes/AccountPage';
import { AuditPage } from './routes/AuditPage';
import { InvitePage } from './routes/InvitePage';
import { PeoplePage } from './routes/PeoplePage';
import { ProjectsPage } from './routes/ProjectsPage';

export const routes = [
  { path: '/login', element: <LoginPage /> },
  { path: '/setup', element: <SetupPage /> },
  { path: '/invite', element: <InvitePage /> },
  {
    element: <RequireAuth />,
    children: [
      {
        element: <ConsoleLayout />,
        children: [
          { path: '/', element: <Navigate to="/apps" replace /> },
          { path: '/settings/tokens', element: <TokensPage /> },
          { path: '/settings/account', element: <AccountPage /> },
          { path: '/settings/people', element: <PeoplePage /> },
          { path: '/settings/projects', element: <ProjectsPage /> },
          { path: '/settings/audit', element: <AuditPage /> },
          { path: '/:section', element: <SectionPage /> },
          { path: '/:section/:id', element: <SectionPage /> },
        ],
      },
    ],
  },
];

const router = createBrowserRouter(routes);

export function App() {
  return <RouterProvider router={router} />;
}
