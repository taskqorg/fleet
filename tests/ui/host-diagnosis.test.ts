import { test } from 'node:test';
import assert from 'node:assert/strict';
import { plainAnswer } from '../../app/src/plain-answer.ts';

// Requirement: refused rows read as words (OR-67); risk: the host detail showed the coordinator's raw JSON answers.
test('plainAnswer drops the JSON of a coordinator answer', () => {
  assert.equal(plainAnswer('409 {"statusCode":409,"error":"Conflict","message":"Content was purged"}\n'), '409 Content was purged');
  assert.equal(plainAnswer('coordinator 400 {"statusCode":400,"error":"Bad Request","message":"Invalid JSON body"}'), '400 Invalid JSON body');
  assert.equal(plainAnswer('no such file'), 'no such file');
});
