import { cn } from "../../lib/utils";

interface LogoProps {
  className?: string;
}

/**
 * Talus brand mark — a padlock with a knocked-out "T".
 *
 * The geometry matches `favicon.svg` (frontend root) so the in-app mark and the
 * browser tab icon are the same shape; keep the two in sync when either changes.
 * The viewBox is tight around the artwork (288x320, ratio 0.9) so the mark fills
 * the box it is given instead of floating in padding.
 *
 * Drawn in `currentColor` with the T cut out via `fill-rule="evenodd"`, so it
 * inherits the surrounding text colour and lets the container background show
 * through the T.
 */
export function Logo({ className }: LogoProps) {
  return (
    <svg
      viewBox="0 0 288 320"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
      focusable="false"
      className={cn("h-6 w-6 shrink-0", className)}
    >
      {/* Shackle */}
      <path
        d="M70 94V54C70 28 101 14 144 14C187 14 218 28 218 54V94"
        stroke="currentColor"
        strokeWidth={28}
        strokeLinecap="round"
      />
      {/* Body with the T knocked out */}
      <path
        d="M36 80H252A36 36 0 0 1 288 116V284A36 36 0 0 1 252 320H36A36 36 0 0 1 0 284V116A36 36 0 0 1 36 80ZM56 140H232V180H175V274H113V180H56Z"
        fill="currentColor"
        fillRule="evenodd"
        clipRule="evenodd"
      />
    </svg>
  );
}
