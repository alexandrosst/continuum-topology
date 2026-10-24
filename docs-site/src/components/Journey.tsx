import Link from '@docusaurus/Link';
import type {ReactNode} from 'react';

/** The strip at the top of a journey page: which step of how many. */
export function StepBar({n, of, title}: {n: number; of: number; title?: string}) {
  return (
    <>
      <div className="ikh-stepbar" aria-hidden>
        {Array.from({length: of}, (_, i) => (
          <span key={i} data-state={i + 1 < n ? 'done' : i + 1 === n ? 'now' : undefined} />
        ))}
      </div>
      <div className="ikh-stepmeta">
        Step {n} of {of}
        {title ? ` · ${title}` : ''}
      </div>
    </>
  );
}

/** What you should see on screen after a step. */
export function See({children}: {children: ReactNode}) {
  return (
    <div className="ikh-see">
      <span className="ikh-see__label">You should see</span>
      {children}
    </div>
  );
}

export function Cards({children}: {children: ReactNode}) {
  return <div className="ikh-cards">{children}</div>;
}

export function Card({to, kicker, title, children}: {to: string; kicker?: string; title: string; children: ReactNode}) {
  return (
    <Link className="ikh-card" to={to}>
      {kicker && <span className="ikh-card__kicker">{kicker}</span>}
      <span className="ikh-card__title">{title}</span>
      <span className="ikh-card__text">{children}</span>
    </Link>
  );
}
