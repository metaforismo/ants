# DESIGN.md

## World: Field Station

Ants' web console reads like a **field station ledger**: the calm, precise
instrument panel of a research post observing an ant colony at work. Mineral
paper ground, ink-drawn structure, one moss-green action color, tabular
monospace for anything measured. The category default — dark canvas, neon
accent, glowing edges — is deliberately refused; so is warm-cream editorial.
Light theme chosen from the use scene: engineers reading dense operational
data for long stretches, often alongside bright code editors.

## Color tokens

| Token | Value | Role |
|---|---|---|
| `--ground` | `#F4F3EE` | page ground |
| `--surface` | `#FFFFFF` | raised panels |
| `--surface-sunken` | `#EFEEE7` | wells, code blocks |
| `--ink` | `#211E17` | primary text (≥15:1 on ground) |
| `--ink-2` | `#5B5546` | secondary text (≥7:1 on surface) |
| `--ink-3` | `#8A8371` | tertiary/meta text (≥4.6:1) |
| `--hairline` | `#DDD9CC` | borders, dividers |
| `--accent` | `#40651A` | actions, active states (white text ≥5.9:1) |
| `--accent-hover` | `#35530F` | pressed/hover accent |
| `--accent-wash` | `#EDF2E2` | selected row wash |
| `--run-live` | `#1D4ED8` | running/active (with label + dot) |
| `--attention` | `#93410E` | needs attention / rate-limited |
| `--danger` | `#B3261E` | failed / destructive |
| `--focus` | `#211E17` | focus outline (2px, offset 2px) |

Status is **never color alone**: every status carries a text label and a
shape-coded dot (running = pulsing ring, attention = diamond, failed = cross,
done = check).

## Type

System font stack only — no downloaded or CDN fonts (no external requests,
nothing vendored without locally verifiable provenance): `system-ui` for UI
voice; `ui-monospace` stack for ids, sequences, commands, and any number a
user might compare. Body measure ≤72ch. Scale: 13px base UI, 15px body prose,
20/28px page titles, mono 12–13px with tabular numerals everywhere data
aligns (`font-variant-numeric: tabular-nums`).

## Space & structure

4px base grid; hairline dividers over shadows; cards radius 14px, controls
10px; one soft shadow reserved for floating layers (dialog, popover). Left
rail navigation on desktop collapses to a top bar + stacked panels on
mobile. More space above headings than below.

## Motion grammar

Purposeful only. One authored moment per surface:

- Buttons press `scale(0.97)` 160ms ease-out; hover shifts are 140ms color-only.
- New timeline entries enter with a 180ms clip-path reveal left→right (the
  "trail"); list rows stagger 40ms capped at first paint only.
- Run status changes crossfade labels 160ms; no movement.
- Keyboard-initiated actions animate nothing (Emil rule).
- `prefers-reduced-motion`: transforms and clip-paths become opacity/color
  fades of ≤120ms; pulses stop.

Easing: `cubic-bezier(0.25, 1, 0.5, 1)` (ease-out-quart) as `--ease-out`;
exits faster than entries (120ms vs 180ms).

## States (every surface ships all of them)

loading → skeleton rows with real layout; empty → named next action;
error → problem code + retry; offline/network → reconnecting banner with
cursor-resume promise; unauthorized → login entry; expired session →
re-authenticate card preserving context; forbidden/not-found → uniform
"not available" copy (no existence oracle); rate-limited → wait state with
Retry-After when present; live run → trail + cancel; terminal report →
evidence-first summary block.

## Component voice

Controls name their action ("Start run", not "Submit"). Errors name the
problem and the recovery. IDs and cursors render in mono. Timestamps render
relative with absolute tooltips. No emoji-as-icons; inline SVG icon set,
one stroke weight (1.5px), drawn in-world.
