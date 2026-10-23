import { createBrowserRouter, RouterProvider } from 'react-router';
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
import { CataloguePage } from './routes/CataloguePage';
import { MachinesPage } from './routes/MachinesPage';
import { OverviewPage } from './routes/OverviewPage';
import { GuidePage } from './routes/GuidePage';

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
          { path: '/', element: <OverviewPage /> },
          { path: '/settings/tokens', element: <TokensPage /> },
          { path: '/settings/account', element: <AccountPage /> },
          { path: '/settings/people', element: <PeoplePage /> },
          { path: '/settings/projects', element: <ProjectsPage /> },
          { path: '/settings/audit', element: <AuditPage /> },
          { path: '/machines', element: <MachinesPage /> },
          { path: '/machines/:name', element: <MachinesPage /> },
          { path: '/catalogue', element: <CataloguePage /> },
          { path: '/guide/:section', element: <GuidePage /> },
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
