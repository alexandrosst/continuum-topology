import ThemedImage from '@theme/ThemedImage';
import useBaseUrl from '@docusaurus/useBaseUrl';
import type {ReactNode} from 'react';

type Props = {
  /** The path under static/img without the theme suffix and extension: "journey/signin" is signin-light.png and signin-dark.png. */
  src: string;
  alt: string;
  caption?: ReactNode;
  ext?: string;
};

/** A real screenshot of the product, in a light and a dark version, in the same frame as a figure. */
export default function Shot({src, alt, caption, ext = 'png'}: Props) {
  const light = useBaseUrl(`/img/${src}-light.${ext}`);
  const dark = useBaseUrl(`/img/${src}-dark.${ext}`);
  return (
    <figure className="ikh-shot">
      <ThemedImage alt={alt} sources={{light, dark}} loading="lazy" />
      {caption && <figcaption>{caption}</figcaption>}
    </figure>
  );
}
