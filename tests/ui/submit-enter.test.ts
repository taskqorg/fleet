// Requirement: a text field submits on Enter (search, decision answer, setting edit, naming a view or preset, composer).
// Risk: the Enter that confirms an IME composition (CJK input) submits half-composed text (modern-web-guidance
// ime-safe-enter-submit).
import test from 'node:test';
import assert from 'node:assert/strict';
import { isSubmitEnter } from '../../packages/ui/src/keys.ts';

test('Enter submits; the Enter that ends an IME composition does not', () => {
  assert.equal(isSubmitEnter({ key: 'Enter', isComposing: false, keyCode: 13 }), true);
  assert.equal(isSubmitEnter({ key: 'Enter', isComposing: true, keyCode: 13 }), false, 'composition still open');
  assert.equal(isSubmitEnter({ key: 'Enter', isComposing: false, keyCode: 229 }), false, "Safari's composing Enter");
  assert.equal(isSubmitEnter({ key: 'a', isComposing: false, keyCode: 65 }), false);
});
