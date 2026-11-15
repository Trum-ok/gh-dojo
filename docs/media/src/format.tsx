import type { Theme } from "./theme";

// Colors a gh-dojo confirmation line ("#42 Backlog → In progress, comment
// left"): the issue in blue, the statuses in purple, the effects muted.
// Partly typed text is colored as far as it goes.
export const confirmation = (t: Theme) => {
  const Confirmation = (text: string) => {
    const m = /^(#\d+)( )?([^→,]*)(→ )?([^,]*)(.*)$/.exec(text);
    if (!m) return <span style={{ color: t.term.fg }}>{text}</span>;
    const [, ref, sp = "", from, arrow = "", to, rest] = m;
    return (
      <span>
        <span style={{ color: t.term.ref, fontWeight: 700 }}>{ref}</span>
        {sp}
        <span style={{ color: t.term.status }}>{from}</span>
        <span style={{ color: t.term.muted }}>{arrow}</span>
        <span style={{ color: t.term.status, fontWeight: 700 }}>{to}</span>
        <span style={{ color: t.term.muted }}>{rest}</span>
      </span>
    );
  };
  return Confirmation;
};
