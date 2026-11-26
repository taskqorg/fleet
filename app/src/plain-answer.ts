// Answers of the coordinator as the host detail shows them (views/host-diagnosis.tsx).
/** An answer as a person reads it: "409 {json}" or "coordinator 400 {json}" becomes "409 Content was purged" (OR-67, no
 *  JSON on screen); other text stays. */
export function plainAnswer(text: string): string {
  const m = /^(?:coordinator )?(\d{3})\s+(\{.*\})\s*$/s.exec(text.trim());
  if (!m) return text.trim();
  try {
    const v = JSON.parse(m[2]) as { message?: unknown; error?: unknown };
    const msg = typeof v.message === 'string' ? v.message : typeof v.error === 'string' ? v.error : '';
    return msg ? `${m[1]} ${msg}` : m[1];
  } catch { return text.trim(); }
}
