import { cn } from "../../lib/utils";

interface LogoProps {
	className?: string;
}

/**
 * Talus brand mark — a padlock with a knocked-out "T".
 *
 * The geometry matches `public/favicon.svg` so the in-app mark and the browser
 * tab icon are the same shape; keep the two in sync when either changes.
 *
 * Drawn in `currentColor` with the T cut out via `fill-rule="evenodd"`, so it
 * inherits the surrounding text colour and lets the container background show
 * through the T. The viewBox is tight around the artwork, so the mark fills
 * the box it is given instead of floating in padding.
 */
export function Logo({ className }: LogoProps) {
	return (
		<svg
			viewBox="4 6 240 320"
			fill="none"
			xmlns="http://www.w3.org/2000/svg"
			aria-hidden="true"
			focusable="false"
			className={cn("h-6 w-6 shrink-0", className)}
		>
			{/* Shackle */}
			<path
				d="M62 100V60C62 34 88 20 124 20C160 20 186 34 186 60V100"
				stroke="currentColor"
				strokeWidth={28}
				strokeLinecap="round"
			/>
			{/* Body with the T knocked out */}
			<path
				d="M40 86H208A36 36 0 0 1 244 122V290A36 36 0 0 1 208 326H40A36 36 0 0 1 4 290V122A36 36 0 0 1 40 86ZM51 146H197V186H150V280H98V186H51Z"
				fill="currentColor"
				fillRule="evenodd"
				clipRule="evenodd"
			/>
		</svg>
	);
}
