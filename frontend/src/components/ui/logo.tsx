import { cn } from "../../lib/utils";

interface LogoProps {
  className?: string;
}

/**
 * Talus brand mark — a padlock with a knocked-out "T".
 *
 * The geometry matches `favicon.svg` (frontend root) so the in-app mark and the
 * browser tab icon are the same shape; keep the two in sync when either changes.
 * The viewBox is tight around the artwork (258x320, ratio 0.806) so the mark
 * fills the box it is given instead of floating in padding.
 *
 * Drawn in `currentColor` with the T cut out via `fill-rule="evenodd"`, so it
 * inherits the surrounding text colour and lets the container background show
 * through the T.
 */
export function Logo({ className }: LogoProps) {
  return (
    <svg
      viewBox="0 0 258 320"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
      focusable="false"
      className={cn("h-6 w-6 shrink-0", className)}
    >
      {/* Shackle */}
      <path
        d="M62 94V54C62 28 91 14 129 14C167 14 196 28 196 54V94"
        stroke="currentColor"
        strokeWidth={28}
        strokeLinecap="round"
      />
      {/* Body with the T knocked out */}
      <path
        d="M36 80H222A36 36 0 0 1 258 116V284A36 36 0 0 1 222 320H36A36 36 0 0 1 0 284V116A36 36 0 0 1 36 80ZM50 140H208V180H157V274H101V180H50Z"
        fill="currentColor"
        fillRule="evenodd"
        clipRule="evenodd"
      />
    </svg>
  );
}
