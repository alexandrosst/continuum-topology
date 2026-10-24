import type {ReactNode} from 'react';

/*
 * The drawing kit every Ikhnos figure is made from.
 *
 * A figure is plain SVG in a React component: nothing is rendered by another program, nothing is exported
 * from a drawing tool, and every colour is a CSS variable, so a figure follows light and dark mode and can
 * be edited as text. The look is one look: 760 units wide, 8-unit corners, a hairline stroke, one accent for
 * "look here", a title and a one-line subtitle. The variables are defined in src/css/custom.css.
 */

export const edge = 'var(--cds-chart-axis)';
export const accent = 'var(--cds-chart-categorical-1)';
export const ink = 'var(--cds-text-primary)';
export const quiet = 'var(--cds-text-secondary)';
export const rule = 'var(--cds-chart-grid)';
export const tint = 'var(--cds-chart-reference-tint)';
export const good = 'var(--cds-chart-status-good)';
export const warn = 'var(--cds-chart-status-warning)';
export const bad = 'var(--cds-chart-status-bad)';
export const cloud = 'var(--cds-tier-cloud)';
export const edgeTier = 'var(--cds-tier-edge)';
export const far = 'var(--cds-tier-far)';

type FigProps = {
  /** A short unique id: it prefixes the arrow markers, so two figures on one page never share one. */
  id: string;
  height: number;
  /** Read by screen readers instead of the drawing. */
  label: string;
  title?: string;
  subtitle?: string;
  children: ReactNode;
};

export function Fig({id, height, label, title, subtitle, children}: FigProps) {
  return (
    <svg viewBox={`0 0 760 ${height}`} role="img" aria-label={label} fontSize="13" className="ikh-fig">
      <defs>
        <marker id={`${id}-arrow`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={edge} />
        </marker>
        <marker id={`${id}-arrow-accent`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0L10 5L0 10z" fill={accent} />
        </marker>
      </defs>
      {title && (
        <text x="24" y="30" fontSize="15" fontWeight="600" fill={ink}>
          {title}
        </text>
      )}
      {subtitle && (
        <text x="24" y="48" fontSize="11.5" fill={quiet}>
          {subtitle}
        </text>
      )}
      {children}
    </svg>
  );
}

type BoxProps = {
  x: number;
  y: number;
  w: number;
  h: number;
  /** Accent outline and a faint accent fill: the thing this figure is about. */
  hi?: boolean;
  /** A colour from the tier or status set, for an outlined box that means something. */
  color?: string;
  dashed?: boolean;
  /** A filled reference band, for storage and other things that are not a step. */
  band?: boolean;
  /** A group frame: hairline in the rule colour, no fill. */
  frame?: boolean;
};

export function Box({x, y, w, h, hi, color, dashed, band, frame}: BoxProps) {
  const stroke = frame ? rule : color ?? (hi ? accent : edge);
  const solidFill = hi || color;
  return (
    <rect
      x={x}
      y={y}
      width={w}
      height={h}
      rx="8"
      fill={band ? tint : solidFill ? (color ?? accent) : 'none'}
      fillOpacity={band ? 1 : solidFill ? 0.12 : 1}
      stroke={stroke}
      strokeWidth={hi || color ? 2 : 1.25}
      strokeDasharray={dashed ? '5 4' : undefined}
    />
  );
}

type TextProps = {
  x: number;
  y: number;
  children: ReactNode;
  bold?: boolean;
  small?: boolean;
  muted?: boolean;
  anchor?: 'start' | 'middle' | 'end';
  color?: string;
  mono?: boolean;
};

/** One line of text. Names are bold at 13, descriptions are 11.5 and muted unless they are the point. */
export function T({x, y, children, bold, small, muted, anchor, color, mono}: TextProps) {
  return (
    <text
      x={x}
      y={y}
      fontSize={small ? 11.5 : undefined}
      fontWeight={bold ? 600 : undefined}
      fill={color ?? (muted ? quiet : ink)}
      textAnchor={anchor}
      fontFamily={mono ? 'var(--ifm-font-family-monospace)' : undefined}
    >
      {children}
    </text>
  );
}

/** Several short lines, 16 units apart, starting at y. Wrap by hand: SVG text does not wrap. */
export function Lines({x, y, lines, muted = true, anchor, color}: {x: number; y: number; lines: string[]; muted?: boolean; anchor?: 'start' | 'middle' | 'end'; color?: string}) {
  return (
    <>
      {lines.map((l, i) => (
        <T key={i} x={x} y={y + i * 16} small muted={muted} anchor={anchor} color={color}>
          {l}
        </T>
      ))}
    </>
  );
}

type ArrowProps = {
  /** SVG path data. */
  d: string;
  fig: string;
  dashed?: boolean;
  both?: boolean;
  accented?: boolean;
  noHead?: boolean;
};

export function Arrow({d, fig, dashed, both, accented, noHead}: ArrowProps) {
  const m = `url(#${fig}-arrow${accented ? '-accent' : ''})`;
  return (
    <path
      d={d}
      fill="none"
      stroke={accented ? accent : edge}
      strokeWidth={accented ? 1.75 : 1.25}
      strokeDasharray={dashed ? '4 4' : undefined}
      markerEnd={noHead ? undefined : m}
      markerStart={both ? m : undefined}
    />
  );
}

/** The small diamond a gate or a decision sits on. */
export function Gate({x, y}: {x: number; y: number}) {
  return <path d={`M${x} ${y - 8}l8 8l-8 8l-8 -8z`} fill="none" stroke={edge} strokeWidth="1.25" />;
}

/** A numbered step badge: a small ring and a number. */
export function Step({x, y, n}: {x: number; y: number; n: number | string}) {
  return (
    <g>
      <circle cx={x} cy={y} r="10" fill="none" stroke={accent} strokeWidth="1.5" />
      <text x={x} y={y + 4} textAnchor="middle" fontSize="11.5" fontWeight="600" fill={ink}>
        {n}
      </text>
    </g>
  );
}

/** A pill: a tier or a state, drawn as an outlined capsule with its colour as text. */
export function Pill({x, y, w, children, color = edge}: {x: number; y: number; w: number; children: ReactNode; color?: string}) {
  return (
    <g>
      <rect x={x} y={y} width={w} height="20" rx="10" fill={color} fillOpacity="0.12" stroke={color} strokeWidth="1.25" />
      <text x={x + w / 2} y={y + 14} textAnchor="middle" fontSize="11" fontWeight="600" fill={ink}>
        {children}
      </text>
    </g>
  );
}
