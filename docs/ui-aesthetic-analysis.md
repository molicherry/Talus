# Talus UI Aesthetic Analysis & Redirection

## Current State

Talus is a server-management dashboard (React + Tailwind v4). The existing UI is functional but visually conservative:

- **Color system**: scattered Tailwind grays (`gray-200`, `gray-800`, `gray-900`) and an indigo/blue CTA that changes between pages.
- **Typography**: system sans-serif, no loaded web font, limited hierarchy.
- **Layout**: classic sidebar + top-bar admin shell. Borders are hard, cards are flat, whitespace is tight.
- **Components**: ad-hoc buttons/inputs across pages; no shared button primitive.
- **Motion**: mostly absent or instant; no consistent easing.
- **Accessibility basics**: dark mode is supported, focus rings are present on inputs, but color contrast and tap-target sizing could be tightened.

## Goal

Modern, minimalist, eye-friendly redesign that feels professional and less tiring during long sessions.

## Design Direction

### 1. Visual Style

- **Soft UI Evolution** + **Bento Grid** influences: rounded cards, generous whitespace, subtle shadows, soft borders.
- Keep both light and dark modes (user may work in either).
- Avoid pure black / pure white; use slightly tinted neutrals for less eye strain.

### 2. Color Palette

Professional, clean, light-first with a balanced dark counterpart:

| Token | Light | Dark | Usage |
| ------- | ------- | ------ | ------- |
| Background | `#FAFBFC` | `#0F1115` | Page canvas |
| Card/Surface | `#FFFFFF` | `#181A20` | Cards, panels, sidebar |
| Text primary | `#111827` | `#F3F4F6` | Headings, body |
| Text secondary | `#4B5563` | `#9CA3AF` | Descriptions, labels |
| Text muted | `#6B7280` | `#6B7280` | Meta, placeholders |
| Border | `#E5E7EB` | `#272A30` | Dividers, input borders |
| Primary | `#2563EB` | `#3B82F6` | CTAs, active nav, links |
| Primary hover | `#1D4ED8` | `#60A5FA` | Hover state |
| Success | `#059669` | `#10B981` | Online, healthy metrics |
| Warning | `#D97706` | `#F59E0B` | Caution, medium load |
| Danger | `#DC2626` | `#EF4444` | Offline, errors, destructive |

### 3. Typography

- **Font**: Inter (Google Fonts) — neutral, highly legible, excellent for dashboards.
- **Scale**: 16 px base; headings 600–700; body 400; labels 500.
- **Line-height**: 1.5 for body, 1.25 for headings.

### 4. Spacing & Shape

- 8 px grid; 16/24/32 px section spacing.
- Card radius: `16 px` (`rounded-2xl`); button/input radius: `10 px` (`rounded-lg` + custom).
- Soft shadow: `0 1px 2px rgba(0,0,0,0.04), 0 1px 3px rgba(0,0,0,0.06)`.

### 5. Interaction

- Transitions: `150 ms` color/shadow, `200 ms` transform.
- Press feedback: subtle scale (`0.98`) or darker overlay on buttons.
- Focus rings: `2 px` offset ring in primary color.
- Loading states: spinner inside buttons, disabled cursor.

### 6. Layout Improvements

- Sidebar: cleaner active item with a left accent indicator + primary color, not just a gray background.
- Header: reduce visual weight, group utilities, better dropdown styling.
- Dashboard cards: larger, softer, bento-style with status badge and metric mini-bars.
- Login: centered, elevated card with better focus and error presentation.

### 7. Accessibility Checklist

- [ ] Contrast ≥ 4.5:1 for normal text.
- [ ] Focus indicators visible on all interactive elements.
- [ ] Icon-only buttons have `aria-label`.
- [ ] Tap targets ≥ 44×44 px.
- [ ] Respect `prefers-reduced-motion`.
- [ ] No emoji as structural icons (already Lucide).

## Implementation Plan

1. Centralize tokens in `frontend/src/index.css` (CSS custom properties + Tailwind v4 `@theme`).
2. Load Inter via Google Fonts.
3. Refactor `MainLayout`, `Sidebar`, `Header` for the new shell.
4. Refactor shared cards/skeletons/empty states in the dashboard.
5. Update `LoginPage` for a modern centered entry.
6. Update `StatusIndicator` and `MetricBar` to use the new semantic tokens.
7. Update `Toaster` to match the theme.
8. Build and deploy.

## Anti-Patterns to Avoid

- Pure white backgrounds in light mode (use `#FAFBFC`).
- Pure black in dark mode (use `#0F1115`).
- Decorative-only animation.
- Hard borders everywhere; prefer subtle shadow + background separation.
- Emoji icons.
- Layout shifts during loading (reserve space with skeletons).
