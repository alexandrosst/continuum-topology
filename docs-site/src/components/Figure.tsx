import clsx from 'clsx';
import type {ReactNode} from 'react';
import {figures} from './figures/registry';

type Props = {
  /** The name of a figure in src/components/figures/registry.tsx. */
  name?: keyof typeof figures;
  /** Or a drawing given directly. */
  children?: ReactNode;
  /** A sentence under the drawing that says what to take from it. */
  caption?: ReactNode;
  /** The widest the drawing is allowed to be, in pixels. Default: the content column. */
  maxWidth?: number;
  /** Let the figure break out of the reading column on wide screens. */
  wide?: boolean;
  className?: string;
};

/**
 * The frame every figure sits in: a card with the drawing on top and a caption under it. The drawing itself is
 * an SVG component from src/components/figures, drawn with the kit in figures/kit.tsx.
 */
export default function Figure({name, children, caption, maxWidth, wide, className}: Props) {
  const Drawing = name ? figures[name] : undefined;
  return (
    <figure className={clsx('ikh-figure', wide && 'ikh-figure--wide', className)}>
      <div className="ikh-figure__stage" style={maxWidth ? {maxWidth} : undefined}>
        {Drawing ? <Drawing /> : children}
      </div>
      {caption && <figcaption className="ikh-figure__caption">{caption}</figcaption>}
    </figure>
  );
}
