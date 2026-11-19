/** Enter that submits a text field: not the Enter that confirms an IME composition (CJK input, the emoji picker), which
 * keydown reports with isComposing (keyCode 229 in Safari). modern-web-guidance: ime-safe-enter-submit. */
export const isSubmitEnter = (e: Pick<KeyboardEvent, 'key' | 'isComposing' | 'keyCode'>) => e.key === 'Enter' && !e.isComposing && e.keyCode !== 229;
