import { Children, Fragment, useId, type HTMLAttributes, type ReactNode, type TableHTMLAttributes } from 'react';
import { ChevronDown } from 'lucide-react';
import { cx } from './primitives';

type Div = Omit<HTMLAttributes<HTMLDivElement>, 'className' | 'style'>;

/** The EnclaveStack mark: a house whose interior is a cloud. Decorative; pair it with the wordmark. */
export function LogoMark({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M5 15 16 6l11 9" />
      <path d="M8 13v12h16V13" />
      <path d="M12.5 21.5a3 3 0 0 1 .4-6 4 4 0 0 1 7.5 1.2 2.4 2.4 0 0 1-.4 4.8z" />
    </svg>
  );
}

export function Brand() {
  return (
    <span className="es-brand">
      <LogoMark />
      EnclaveStack
    </span>
  );
}

/**
 * A floating sidebar beside the main column (L2.5-002 §16). `rail` folds the sidebar to its icons. On narrow
 * screens the sidebar becomes a sheet opened from the top bar.
 */
export function Shell({ sidebar, children, menuOpen, onCloseMenu, rail = false }: { sidebar: ReactNode; children: ReactNode; menuOpen: boolean; onCloseMenu: () => void; rail?: boolean }) {
  return (
    <div className="es-shell" data-rail={rail}>
      {menuOpen && <button type="button" className="es-shell-scrim" aria-label="Close menu" onClick={onCloseMenu} />}
      {sidebar}
      <div className="es-main">{children}</div>
    </div>
  );
}

export function Sidebar({ label, open, children }: { label: string; open: boolean; children: ReactNode }) {
  return <aside className="es-sidebar" data-open={open} aria-label={label}>{children}</aside>;
}

export function SidebarSection({ heading, children }: { heading?: string; children: ReactNode }) {
  return (
    <div className="es-sidebar-section">
      {heading && <p className="es-sidebar-heading">{heading}</p>}
      {children}
    </div>
  );
}

export function SidebarFoot({ children }: { children: ReactNode }) {
  return <div className="es-sidebar-foot">{children}</div>;
}

/** Class for the router link wrapping NavItemContent; the active state is aria-current="page". */
export const navItemClass = 'es-nav-item';

/**
 * A heading that opens and closes its group of links (CON-61). The group holding the current page stays
 * open: pass `open` and `onToggle` from the app, which remembers the choice.
 */
export function NavGroup({ heading, open, onToggle, children }: { heading: string; open: boolean; onToggle: () => void; children: ReactNode }) {
  const id = useId();
  return (
    <div className="es-nav-group" data-open={open}>
      <button type="button" className="es-nav-group-head" aria-expanded={open} aria-controls={id} onClick={onToggle}>
        <span>{heading}</span>
        <ChevronDown aria-hidden="true" />
      </button>
      <div className="es-nav-group-body" id={id}>
        <div className="es-nav-group-inner">
          <nav aria-label={heading}><div className="es-nav">{children}</div></nav>
        </div>
      </div>
    </div>
  );
}

/** Icon, label and count inside a navigation link. In the rail only the icon shows; the label stays for assistive technology. */
export function NavItemContent({ icon, label, count, countLabel }: { icon: ReactNode; label: string; count?: ReactNode; countLabel?: string }) {
  return (
    <>
      <span className="es-nav-icon" aria-hidden="true">{icon}</span>
      <span className="es-nav-label">{label}</span>
      {count !== undefined && <span className="es-nav-count" aria-label={countLabel}>{count}</span>}
    </>
  );
}

export function NavList({ label, children }: { label: string; children: ReactNode }) {
  return <nav aria-label={label}><div className="es-nav">{children}</div></nav>;
}

export function MachineCard({ children }: { children: ReactNode }) {
  return <div className="es-machine">{children}</div>;
}

export function MeterCard({ children }: { children: ReactNode }) {
  return <div className="es-meter">{children}</div>;
}

export function UserRow({ children }: { children: ReactNode }) {
  return <div className="es-user">{children}</div>;
}

export function Topbar({ children }: { children: ReactNode }) {
  return <header className="es-topbar">{children}</header>;
}

export function Crumbs({ children }: { children: ReactNode }) {
  return <div className="es-crumbs">{children}</div>;
}

/** Class for the router links inside Breadcrumbs. */
export const crumbLinkClass = 'es-crumb-link';

/**
 * Where you are, every step but the last a link (CON-63). Pass the steps as children: router links with
 * crumbLinkClass, and the current page as plain text.
 */
export function Breadcrumbs({ children }: { children: ReactNode }) {
  const items = Children.toArray(children);
  return (
    <nav aria-label="Breadcrumb" className="es-breadcrumb">
      <ol>
        {items.map((item, i) => (
          <Fragment key={i}>
            <li aria-current={i === items.length - 1 ? 'page' : undefined}>{item}</li>
            {i < items.length - 1 && <li aria-hidden="true" className="es-breadcrumb-sep">/</li>}
          </Fragment>
        ))}
      </ol>
    </nav>
  );
}

/**
 * A page's header (CON-63): back link, title with its state, a line saying what this is, chips that carry
 * meaning, and the page's actions; `aside` holds key facts. Labelled so tests and assistive technology find
 * the summary.
 */
export function PageHeader({ label, back, title, status, lede, meta, actions, aside }: {
  label: string; back?: ReactNode; title: ReactNode; status?: ReactNode; lede?: ReactNode; meta?: ReactNode; actions?: ReactNode; aside?: ReactNode;
}) {
  return (
    <header className="es-page-header" role="group" aria-label={label}>
      <div className="es-page-header-main">
        <div className="es-page-header-text">
          {back && <div className="es-page-header-back">{back}</div>}
          <div className="es-page-header-title">
            <h1 className="es-page-title">{title}</h1>
            {status}
          </div>
          {lede && <p className="es-page-lede">{lede}</p>}
          {meta && <div className="es-page-header-meta">{meta}</div>}
        </div>
        {actions && <div className="es-page-header-actions">{actions}</div>}
      </div>
      {aside}
    </header>
  );
}

export function Banner({ children }: { children: ReactNode }) {
  return <div className="es-banner" role="status">{children}</div>;
}

/** List column + detail panel. data-has-selection drives which one shows on phones. */
export function Workspace({ hasSelection, children }: { hasSelection: boolean; children: ReactNode }) {
  return <div className="es-workspace" data-has-selection={hasSelection}>{children}</div>;
}

export function ListHead({ children }: { children: ReactNode }) {
  return <div className="es-list-head">{children}</div>;
}

export function List({ children, ...rest }: Div) {
  return <div className="es-list" {...rest}>{children}</div>;
}

/** Class for the router link wrapping ListCardContent; the selected card has aria-current="true". */
export const listCardClass = 'es-list-card';

/** Avatar, name, meta line and trailing badge inside a list card link. */
export function ListCardContent({ avatar, name, meta, trailing }: { avatar: ReactNode; name: string; meta: ReactNode; trailing?: ReactNode }) {
  return (
    <>
      {avatar}
      <span className="es-list-card-body">
        <span className="es-list-card-name">{name}</span>
        <span className="es-list-card-meta">{meta}</span>
      </span>
      {trailing}
    </>
  );
}

/** The wash header of the detail panel. Labelled so tests and assistive technology can find the summary. */
export function DetailHead({ label, children }: { label: string; children: ReactNode }) {
  return <div className="es-detail-head" role="group" aria-label={label}>{children}</div>;
}

export function DetailToolbar({ children }: { children: ReactNode }) {
  return <div className="es-detail-toolbar">{children}</div>;
}

/** Title block on the left, key facts on the right. */
export function DetailHero({ children, aside }: { children: ReactNode; aside?: ReactNode }) {
  return (
    <div className="es-detail-hero">
      <div className="es-stack es-gap-3">{children}</div>
      {aside && <div>{aside}</div>}
    </div>
  );
}

export function DetailTitle({ children }: { children: ReactNode }) {
  return <h1 className="es-detail-title">{children}</h1>;
}

export function DetailProgress({ children }: { children: ReactNode }) {
  return <div className="es-detail-progress">{children}</div>;
}

export function DetailBody({ children }: { children: ReactNode }) {
  return <div className="es-detail-body">{children}</div>;
}

/** Shown only on phones: the way back from a detail to its list. */
export function BackSlot({ children }: { children: ReactNode }) {
  return <div className="es-back">{children}</div>;
}

export function Page({ children }: { children: ReactNode }) {
  return <div className="es-page">{children}</div>;
}

export function PageHead({ title, lede }: { title: ReactNode; lede?: ReactNode }) {
  return (
    <div className="es-page-head">
      <h1 className="es-text es-size-3xl es-weight-heavy">{title}</h1>
      {lede && <p className="es-text es-size-md es-tone-muted">{lede}</p>}
    </div>
  );
}

export function Table({ children, ...rest }: Omit<TableHTMLAttributes<HTMLTableElement>, 'className' | 'style'>) {
  return (
    <div className="es-table-wrap">
      <table className="es-table" {...rest}>{children}</table>
    </div>
  );
}

/** Split sign-in / setup layout: a washed panel that introduces the product, and the form. */
export function AuthLayout({ title, lede, aside, children }: { title: string; lede: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <div className="es-auth">
      <section className="es-auth-aside" aria-label="About EnclaveStack">
        <Brand />
        <div className="es-stack es-gap-4">
          <h1 className="es-auth-title">{title}</h1>
          <p className="es-text es-size-lg es-tone-muted">{lede}</p>
        </div>
        {aside}
      </section>
      <main className="es-auth-main">
        <div className="es-auth-card">{children}</div>
      </main>
    </div>
  );
}

export function Splash({ children }: { children: ReactNode }) {
  return <div className={cx('es-splash')} role="status">{children}</div>;
}
